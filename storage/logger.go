package storage

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
	_ "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type Logger struct {
	ZapLogger     *zap.Logger
	LogLevel      gormlogger.LogLevel
	SlowThreshold time.Duration
	ContextFn     func(ctx context.Context) []zapcore.Field
	mu            sync.Mutex
}

func InitLogger(logFilePath string, level gormlogger.LogLevel) *Logger {
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.TimeKey = "timestamp"
	encoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout("Jan 02 15:04:05.000000000")

	fileWriter := &lumberjack.Logger{
		Filename:  logFilePath,
		MaxSize:   100, // megabytes (hardcoded)
		MaxAge:    90,  // days (hardcoded)
		LocalTime: true,
		Compress:  true,
	}

	// Create a multi-write syncer to write logs to multiple outputs if desired
	writeSyncer := zapcore.NewMultiWriteSyncer(zapcore.AddSync(fileWriter))

	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		writeSyncer,
		zap.NewAtomicLevelAt(zapcore.Level(level)),
	)
	if core == nil {
		panic("core is nil")
	}

	logger := zap.New(core, zap.AddCaller(), zap.AddCallerSkip(1))

	return &Logger{
		ZapLogger:     logger,
		LogLevel:      level,
		SlowThreshold: 100 * time.Millisecond,
		ContextFn:     nil,
		mu:            sync.Mutex{},
	}
}

func (l *Logger) Info(ctx context.Context, str string, args ...interface{}) {
	if l.LogLevel < gormlogger.Info {
		return
	}
	l.logger(ctx).Info(fmt.Sprintf(str, args...))
}

func (l *Logger) Warn(ctx context.Context, str string, args ...interface{}) {
	if l.LogLevel < gormlogger.Warn {
		return
	}
	l.logger(ctx).Warn(fmt.Sprintf(str, args...))
}

func (l *Logger) Error(ctx context.Context, str string, args ...interface{}) {
	if l.LogLevel < gormlogger.Error {
		return
	}
	l.logger(ctx).Error(fmt.Sprintf(str, args...))
}

func (l *Logger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if l.LogLevel <= 0 {
		return
	}
	elapsed := time.Since(begin)

	switch {
	case err != nil && l.LogLevel >= gormlogger.Error && !errors.Is(err, gorm.ErrRecordNotFound):
		sql, rows := fc()
		l.logger(ctx).Error("trace", zap.Error(err), zap.Duration("elapsed", elapsed), zap.Int64("rows", rows), zap.String("sql", sql))
	case l.SlowThreshold != 0 && elapsed > l.SlowThreshold && l.LogLevel >= gormlogger.Warn:
		sql, rows := fc()
		l.logger(ctx).Warn("trace", zap.Duration("elapsed", elapsed), zap.Int64("rows", rows), zap.String("sql", sql))
	case l.LogLevel >= gormlogger.Info:
		sql, rows := fc()
		l.logger(ctx).Debug("trace", zap.Duration("elapsed", elapsed), zap.Int64("rows", rows), zap.String("sql", sql))
	}
}

func (l *Logger) logger(ctx context.Context) *zap.Logger {
	logger := l.ZapLogger
	if l.ContextFn != nil {
		fields := l.ContextFn(ctx)
		logger = logger.With(fields...)
	}
	return logger
}

func (logger *Logger) ErrorLog(ctx context.Context, msg string, fields ...zapcore.Field) {
	logger.mu.Lock()
	defer logger.mu.Unlock()
	logger.ZapLogger.Error(msg, fields...)
}

func (logger *Logger) InfoLog(ctx context.Context, msg string, fields ...zapcore.Field) {
	logger.mu.Lock()
	defer logger.mu.Unlock()
	logger.ZapLogger.Info(msg, fields...)
}

// e.g. logger.Info(context.Background(), "User %s with id %d logged in.", "alice", 123)
// OR : logger.InfoLog(context.Background(), "User login", zap.String("user", "alice"), zap.Int("id", 123))

// you need this to mirror of the GORM logger signature
func (l *Logger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	l.LogLevel = level
	return l
}