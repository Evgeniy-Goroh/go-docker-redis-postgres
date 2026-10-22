package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jessevdk/go-flags"
	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"github.com/segmentio/kafka-go"
)

type Options struct {
	Config string `short:"c" long:"config" default:"config.yml" description:"Path to configuration file"`
}

var (
	options = Options{}
	parser  = flags.NewParser(&options, flags.Default)

	app  *http.Server
	once sync.Once
)

// initRedis — подключение к Redis с проверкой.
func initRedis(ctx context.Context) (*redis.Client, error) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: os.Getenv("REDIS_PASSWORD"),
	})

	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	if err := rdb.Ping(pingCtx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("redis ping: %w", err)
	}

	return rdb, nil
}

// initDB — подключение к Postgres с проверкой.
func initDB(ctx context.Context) (*sql.DB, error) {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		dsn = "postgres://mg_vk:mg_vk@127.0.0.1:5434/mg_vk?sslmode=disable"
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("sql open: %w", err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("db ping: %w", err)
	}

	return db, nil
}

// initKafka — проверяет доступность брокера Kafka.
// Возвращает адрес брокера (для healthHandler) и ошибку, если брокер недоступен.
func initKafka(ctx context.Context) (string, error) {
	brokers := os.Getenv("KAFKA_BROKERS")
	if brokers == "" {
		brokers = "127.0.0.1:9092"
	}

	// Проверяем доступность: запрашиваем метаданные (список топиков).
	// Если брокер недоступен — вернётся ошибка.
	conn, err := kafka.DialContext(ctx, "tcp", brokers)
	if err != nil {
		return "", fmt.Errorf("kafka dial: %w", err)
	}
	defer conn.Close()

	// Контекст на чтение метаданных
	metaCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	if _, err := conn.ReadPartitions(); err != nil {
		return "", fmt.Errorf("kafka read partitions: %w", err)
	}
	_ = metaCtx

	return brokers, nil
}

// healthHandler — проверяет Redis и Postgres.
func healthHandler(rdb *redis.Client, db *sql.DB, kafkaBrokers string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		status := map[string]string{"status": "ok"}
		code := http.StatusOK

		if err := rdb.Ping(ctx).Err(); err != nil {
			status["redis"] = "unavailable"
			code = http.StatusServiceUnavailable
		} else {
			status["redis"] = "ok"
		}

		if err := db.PingContext(ctx); err != nil {
			status["postgres"] = "unavailable"
			code = http.StatusServiceUnavailable
		} else {
			status["postgres"] = "ok"
		}

		// Kafka
		if err := checkKafka(ctx, kafkaBrokers); err != nil {
			status["kafka"] = "unavailable"
			code = http.StatusServiceUnavailable
		} else {
			status["kafka"] = "ok"
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(status)
	}
}

// checkKafka — лёгкая проверка доступности брокера.
func checkKafka(ctx context.Context, brokers string) error {
	conn, err := kafka.DialContext(ctx, "tcp", brokers)
	if err != nil {
		return err
	}
	defer conn.Close()

	// ReadPartitions — быстрый способ убедиться, что брокер отвечает.
	_, err = conn.ReadPartitions()
	return err
}

func helloHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("Привет, мир!\n"))
}

func initServer(configPath string, rdb *redis.Client, db *sql.DB, kafkaBrokers string) {
	once.Do(func() {
		_ = configPath

		mux := http.NewServeMux()
		mux.HandleFunc("/health", healthHandler(rdb, db, kafkaBrokers))
		mux.HandleFunc("/hello", helloHandler)

		addr := os.Getenv("HTTP_ADDR")
		if addr == "" {
			addr = ":3001"
		}

		app = &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		}
	})
}

func main() {
	if _, err := parser.Parse(); err != nil {
		if flagsErr, ok := err.(*flags.Error); ok && flagsErr.Type == flags.ErrHelp {
			os.Exit(0)
		}
		os.Exit(1)
	}

	ctx := context.Background()

	rdb, err := initRedis(ctx)
	if err != nil {
		log.Fatalf("redis: %v", err)
	}
	defer func() {
		if err := rdb.Close(); err != nil {
			log.Printf("redis close: %v", err)
		}
	}()

	db, err := initDB(ctx)
	if err != nil {
		log.Fatalf("postgres: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("db close: %v", err)
		}
	}()

	kafkaBrokers, err := initKafka(ctx)
	if err != nil {
		log.Fatalf("kafka: %v", err)
	}

	initServer(options.Config, rdb, db, kafkaBrokers)

	errCh := make(chan error, 1)
	go func() {
		defer close(errCh)
		errCh <- app.ListenAndServe()
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			log.Printf("server: %v", err)
		}
	case <-stop:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := app.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
		}
		cancel()
	}
}
