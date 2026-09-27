package mistral

import (
	"context"
	"net/http"
	"time"
)

// SetSleep replaces the retry sleeper so tests never wait for real.
func SetSleep(p *Provider, sleep func(ctx context.Context, d time.Duration) error) {
	p.sleepFor = sleep
}

// SetMaxResponseBytes lowers the success-body cap so tests stay small.
func SetMaxResponseBytes(p *Provider, n int64) { p.maxBody = n }

func ClientOf(p *Provider) *http.Client { return p.client }

const MaxRetryWait = maxRetryWait
