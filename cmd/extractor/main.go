package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/Kirill-Katz/interview-reco/internal"
	"github.com/joho/godotenv"
)

func main() {
	outputDirectory := flag.String("output-dir", "./data", "Directory for saved Asana JSON files")
	projectsInterval := flag.Duration("projects-interval", time.Minute, "Interval between project sync ticks")
	usersInterval := flag.Duration("users-interval", time.Minute, "Interval between user sync ticks")
	debug := flag.Bool("debug", false, "Enable debug logging")
	var retry internal.RetryConfig
	flag.DurationVar(&retry.MaxJitter, "max-jitter", time.Second, "Maximum additive retry jitter")
	flag.DurationVar(&retry.MaxBackoff, "max-backoff", 30*time.Second, "Maximum exponential retry backoff before jitter")
	flag.IntVar(&retry.MaxAttempts, "max-attempts", 5, "Maximum attempts per page, including the initial attempt")
	flag.Parse()

	logger := newLogger(os.Stderr, *debug)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, *outputDirectory, *projectsInterval, *usersInterval, retry); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("Asana integration failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, outputDirectory string, projectsInterval, usersInterval time.Duration, retry internal.RetryConfig) error {
	if outputDirectory == "" {
		return errors.New("-output-dir is required")
	}
	config, err := readAPIConfig()
	if err != nil {
		return err
	}

	client, err := internal.NewAsanaAPIClient(
		&http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		config,
		retry,
	)

	if err != nil {
		return err
	}

	store, err := internal.NewAsanaJsonStore(outputDirectory)
	if err != nil {
		return err
	}
	integration, err := internal.NewAsanaIntegration(client, store)
	if err != nil {
		return err
	}
	jobs := []internal.Job{
		{
			Name:     "projects",
			Interval: projectsInterval,
			PollFunc: integration.SyncProjects,
		},
		{
			Name:     "users",
			Interval: usersInterval,
			PollFunc: integration.SyncUsers,
		},
	}
	poller, err := internal.NewPoller(jobs, func(name string, err error) {
		slog.Error("Asana sync failed", "job", name, "error", err)
	})
	if err != nil {
		return err
	}
	return poller.Run(ctx)
}

func readAPIConfig() (internal.APIConfig, error) {
	values, err := godotenv.Read(".env")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		// A dotenv parse error may include secret file contents.
		return internal.APIConfig{}, errors.New("could not read .env")
	}
	lookup := func(key string) string {
		if value, ok := os.LookupEnv(key); ok {
			return value
		}
		return values[key]
	}
	return internal.APIConfig{
		Token:       lookup("API_TOKEN"),
		WorkspaceID: lookup("WORKSPACE_ID"),
		Host:        lookup("API_HOST"),
	}, nil
}
