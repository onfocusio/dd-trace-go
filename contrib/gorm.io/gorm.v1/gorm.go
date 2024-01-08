// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016 Datadog, Inc.

// Package gorm provides helper functions for tracing the gorm.io/gorm package (https://github.com/go-gorm/gorm).
package gorm

import (
	"context"
	"math"

	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/ext"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
	"gopkg.in/DataDog/dd-trace-go.v1/internal/log"
	"gopkg.in/DataDog/dd-trace-go.v1/internal/telemetry"

	"gorm.io/gorm"
)

const componentName = "gorm.io/gorm.v1"

func init() {
	telemetry.LoadIntegration(componentName)
	tracer.MarkIntegrationImported(componentName)
}

type key string

const (
	gormSpan = key("dd-trace-go:span")
)

type GormTracePlugin struct {
	options []Option
}

func New(opts ...Option) GormTracePlugin {
	return GormTracePlugin{
		options: opts,
	}
}

func (GormTracePlugin) Name() string {
	return "GormTracePlugin"
}

func (g GormTracePlugin) Initialize(db *gorm.DB) error {
	_, err := withCallbacks(db, g.options...)
	return err
}

// Open opens a new (traced) database connection. The used driver must be formerly registered
// using (gopkg.in/DataDog/dd-trace-go.v1/contrib/database/sql).Register.
func Open(dialector gorm.Dialector, cfg *gorm.Config, opts ...Option) (*gorm.DB, error) {
	db, err := gorm.Open(dialector, cfg)
	if err != nil {
		return db, err
	}
	return withCallbacks(db, opts...)
}

func withCallbacks(db *gorm.DB, opts ...Option) (*gorm.DB, error) {
	cfg := new(config)
	defaults(cfg)
	for _, fn := range opts {
		fn(cfg)
	}
	log.Debug("Registering Callbacks: %#v", cfg)

	afterFunc := func() func(*gorm.DB) {
		return func(db *gorm.DB) {
			after(db, cfg)
		}
	}

	beforeFunc := func(operationName string) func(*gorm.DB) {
		return func(db *gorm.DB) {
			before(db, operationName, cfg)
		}
	}

	cb := db.Callback()
	err := cb.Create().Before("*").Register("dd-trace-go:before_create", beforeFunc("gorm.create"))
	if err != nil {
		return db, err
	}
	err = cb.Create().After("*").Register("dd-trace-go:after_create", afterFunc())
	if err != nil {
		return db, err
	}
	err = cb.Update().Before("*").Register("dd-trace-go:before_update", beforeFunc("gorm.update"))
	if err != nil {
		return db, err
	}
	err = cb.Update().After("*").Register("dd-trace-go:after_update", afterFunc())
	if err != nil {
		return db, err
	}
	err = cb.Delete().Before("*").Register("dd-trace-go:before_delete", beforeFunc("gorm.delete"))
	if err != nil {
		return db, err
	}
	err = cb.Delete().After("*").Register("dd-trace-go:after_delete", afterFunc())
	if err != nil {
		return db, err
	}
	err = cb.Query().Before("*").Register("dd-trace-go:before_query", beforeFunc("gorm.query"))
	if err != nil {
		return db, err
	}
	err = cb.Query().After("*").Register("dd-trace-go:after_query", afterFunc())
	if err != nil {
		return db, err
	}
	err = cb.Row().Before("*").Register("dd-trace-go:before_row_query", beforeFunc("gorm.row_query"))
	if err != nil {
		return db, err
	}
	err = cb.Row().After("*").Register("dd-trace-go:after_row_query", afterFunc())
	if err != nil {
		return db, err
	}
	err = cb.Raw().Before("*").Register("dd-trace-go:before_raw_query", beforeFunc("gorm.raw_query"))
	if err != nil {
		return db, err
	}
	err = cb.Raw().After("*").Register("dd-trace-go:after_raw_query", afterFunc())
	if err != nil {
		return db, err
	}
	return db, nil
}

func before(scope *gorm.DB, operationName string, cfg *config) {
	if scope.Statement == nil && scope.Statement.Context == nil {
		return
	}

	ctx := scope.Statement.Context

	opts := []ddtrace.StartSpanOption{
		tracer.ServiceName(cfg.serviceName),
		tracer.SpanType(ext.SpanTypeSQL),
		tracer.Tag(ext.Component, componentName),
	}

	span, ctx := tracer.StartSpanFromContext(ctx, operationName, opts...)
	scope.Statement.Context = ctx
	scope.Statement.Context = context.WithValue(scope.Statement.Context, gormSpan, span)
}

func after(db *gorm.DB, cfg *config) {
	if db.Statement == nil || db.Statement.Context == nil {
		return
	}

	ctx := db.Statement.Context
	span, ok := ctx.Value(gormSpan).(ddtrace.Span)
	if !ok {
		return
	}

	span.SetTag(ext.ResourceName, db.Statement.SQL.String())

	if !math.IsNaN(cfg.analyticsRate) {
		span.SetTag(ext.EventSampleRate, cfg.analyticsRate)
	}
	for key, tagFn := range cfg.tagFns {
		if tagFn != nil {
			span.SetTag(key, tagFn(db))
		}
	}

	var dbErr error
	if cfg.errCheck(db.Error) {
		dbErr = db.Error
	}
	span.Finish(tracer.WithError(dbErr))
}
