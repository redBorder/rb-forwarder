package rbforwarder

import (
	"bytes"
	"sync/atomic"
	"time"

	"github.com/Sirupsen/logrus"
)

// Version is the current tag
var Version = "0.4-beta3"

var log = logrus.New()

// Logger for the package
var Logger = logrus.NewEntry(log)

//------------------------------------------------------------------------------
// RBForwarder
//------------------------------------------------------------------------------

// Config stores the configuration for a forwarder
type Config struct {
	Retries     int
	Backoff     int
	Workers     int
	QueueSize   int
	MaxMessages int
	MaxBytes    int
	ShowCounter int
}

// RBForwarder is the main objecto of the package. It has the main methods for
// send messages and get reports. It has a backend for routing messages between
// workers
type RBForwarder struct {
	backend       *backend
	reportHandler *reportHandler
	reports       chan Report
	counter       uint64

	config Config
}

// NewRBForwarder creates a new Forwarder object
func NewRBForwarder(config Config) *RBForwarder {
	backend := &backend{
		workers:     config.Workers,
		queue:       config.QueueSize,
		maxMessages: config.MaxMessages,
		maxBytes:    config.MaxBytes,
	}

	forwarder := &RBForwarder{
		backend: backend,
		reports: make(chan Report, config.QueueSize),
		config:  config,
	}

	forwarder.reportHandler = newReportHandler(
		config.Retries,
		config.Backoff,
		config.QueueSize,
		backend.input,
	)

	fields := logrus.Fields{
		"workers":      config.Workers,
		"retries":      config.Retries,
		"backoff_time": config.Backoff,
		"queue_size":   config.QueueSize,
		"max_messages": config.MaxMessages,
		"max_bytes":    config.MaxBytes,
	}

	Logger.WithFields(fields).Debug("Initialized rB Forwarder")

	return forwarder
}

// Start spawning workers
func (f *RBForwarder) Start() {

	// Start the backend
	f.backend.Init()

	// Start the report handler
	f.reportHandler.Init()

	if f.config.ShowCounter > 0 {
		go func() {
			for {
				timer := time.NewTimer(
					time.Duration(f.config.ShowCounter) * time.Second,
				)
				<-timer.C
				if f.counter > 0 {
					Logger.Infof(
						"Messages per second %d",
						f.counter/uint64(f.config.ShowCounter),
					)
					f.counter = 0
				}
			}
		}()
	}

	// Get reports from the backend and send them to the reportHandler
	done := make(chan struct{})
	go func() {
		done <- struct{}{}
		for message := range f.backend.reports {
			if message.Report.StatusCode == 0 {
				atomic.AddUint64(&f.counter, 1)
			}
			f.reportHandler.in <- message
		}
	}()
	<-done

	// Listen for reutilizable messages and send them back to the pool
	go func() {
		done <- struct{}{}
		for message := range f.reportHandler.freedMessages {
			f.backend.messagePool <- message
		}
	}()
	<-done
}

// Close stop pending actions
func (f *RBForwarder) Close() {
	f.backend.active = false
	f.reportHandler.close <- struct{}{}
}

// SetSenderHelper set a sender on the backend
func (f *RBForwarder) SetSenderHelper(SenderHelper SenderHelper) {
	f.backend.senderHelper = SenderHelper
}

// GetReports is used by the source to get a report for a sent message.
// Reports are delivered on the same order that was sent
func (f *RBForwarder) GetReports() <-chan Report {
	return f.reportHandler.GetReports()
}

// GetOrderedReports is the same as GetReports() but the reports are delivered
// in order
func (f *RBForwarder) GetOrderedReports() <-chan Report {
	return f.reportHandler.GetOrderedReports()
}

// Produce is used by the source to send messages to the backend
func (f *RBForwarder) Produce(buf []byte) error {
	message := <-f.backend.messagePool

	message.InputBuffer = bytes.NewBuffer(buf)

	message.Report = Report{
		ID:       atomic.AddUint64(&f.backend.currentProducedID, 1) - 1,
		Metadata: message.Metadata,
	}

	f.backend.input <- message

	return nil
}
