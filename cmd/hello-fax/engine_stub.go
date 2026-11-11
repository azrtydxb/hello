//go:build !spandsp || !cgo

package main

import "context"

// newEngine is the build without spandsp: every session fails, so the
// service reports itself unavailable and hello-sip answers fax calls 488.
func newEngine() Engine { return stubEngine{} }

type stubEngine struct{}

func (stubEngine) Run(context.Context, Job) (Outcome, error) { return Outcome{}, ErrNoEngine }

// engineAvailable reports whether sessions can run.
const engineAvailable = false
