package rubban

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/robfig/cron/v3"
	"github.com/sherifabdlnaby/rubban/config"
	"github.com/sherifabdlnaby/rubban/log"
	"github.com/sherifabdlnaby/rubban/rubban/kibana"
)

type rubban struct {
	config           *config.Config
	logger           log.Logger
	client           *kibana.Client
	semVer           semver.Version
	api              kibana.API
	scheduler        *cron.Cron
	autoIndexPattern AutoIndexPattern
}

// Main is the main function of the application, it will be run by cobra's root command.
func Main() {

	// Create App
	rubban := rubban{}

	mainCtx, cancel := context.WithCancel(context.Background())

	shutdownSignal := make(chan struct{})
	go func() {
		rubban.terminateOnSignal(cancel)
		shutdownSignal <- struct{}{}
	}()

	err := rubban.Initialize(mainCtx)
	if err != nil {
		panic("Failed to Initialize rubban. Error: " + err.Error())
	}

	// Register Scheduler
	rubban.RegisterSchedulers()

	// Wait to Shutdown
	<-shutdownSignal

	// Sync Logger and Close.
	_ = rubban.logger.Sync()
	rubban.logger.Infof("Goodbye. <3")

	os.Exit(0)
}

func (b *rubban) Initialize(ctx context.Context) error {

	var err error

	// Get Default logger
	logger := log.Default()

	// Load config
	b.config, err = config.Load("rubban")
	if err != nil {
		logger.Fatalw("Failed to load configuration.", "error", err)
		os.Exit(1)
	}

	// Init logger
	b.logger = log.NewZapLoggerImpl("rubban", b.config.Logging)
	b.logger.Info("Starting rubban...")

	// Init scheduler
	b.scheduler = cron.New()
	b.scheduler.Start()

	// Init Kibana API Client
	b.logger.Info("Initializing Kibana API Client...")
	b.client, err = kibana.NewKibanaClient(b.config.Kibana, b.logger.Extend("client"))
	if err != nil {
		b.logger.Fatalw("Could not Initialize Kibana API Client", "error", err.Error())
	}

	// Validate Connection
	if err = b.client.Validate(ctx, 5, 10*time.Second); err != nil {
		err = fmt.Errorf("couldn't validate connection to Kibana API")
		b.logger.Fatal("Cannot Initialize rubban without an Initial Connection to Kibana API")
		return err
	}
	b.logger.Info("Validated Initial Connection to Kibana API")

	// Get Kibana Version (To Determine which set of APIs to use later)
	b.semVer, err = b.client.GuessVersion()
	if err != nil {
		err = fmt.Errorf("couldn't determine kibana version")
		b.logger.Fatal(strings.ToTitle(err.Error()))
		return err
	}
	b.logger.Infow(fmt.Sprintf("Determined Kibana Version: %s", b.semVer.String()))

	// Determine API
	// TODO for now rubban only support API V7
	b.api = kibana.NewAPIVer7(b.client)

	// Init RunAutoIndexPattern
	if b.config.AutoIndexPattern.Enabled {
		b.autoIndexPattern = *NewAutoIndexPattern(b.config.AutoIndexPattern)
		b.logger.Infow(fmt.Sprintf("Loaded %d General Patterns for Auto Index Patterns Creation", len(b.autoIndexPattern.GeneralPatterns)))
	}

	return nil
}

func (b *rubban) terminateOnSignal(cancel context.CancelFunc) {

	// Signal Channels
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)

	// Termination
	sig := <-signalChan
	b.logger.Infof("Received %s signal, b is shutting down...", sig.String())

	// cancel context
	cancel()

	ctx := b.scheduler.Stop()
	// Wait for Running Jobs to finish.
	select {
	case <-ctx.Done():
		break
	default:
		b.logger.Infof("Waiting for running jobs to finish...")
		<-ctx.Done()
	}
}
