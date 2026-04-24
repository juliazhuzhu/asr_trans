package log

import (
	"io"
	"os"
	"strings"
	"time"

	rotatelogs "github.com/lestrrat-go/file-rotatelogs"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	WriterStdOut = "stdout"
	WriterFile   = "file"
)

const (
	RotateTimeDaily  = "daily"
	RotateTimeHourly = "hourly"
)

type zapLogger struct {
	sugaredLogger *zap.SugaredLogger
}

func newZapLogger(cfg *Config) (Logger, error) {
	encoder := getJSONEncoder()

	var cores []zapcore.Core
	var options []zap.Option

	option := zap.Fields(zap.String("app", cfg.AppName))
	options = append(options, option)

	writers := strings.Split(cfg.Writers, ",")
	for _, w := range writers {
		w = strings.TrimSpace(w)
		if w == WriterStdOut {
			stdoutEncoder := getConsoleEncoder()
			core := zapcore.NewCore(stdoutEncoder, zapcore.AddSync(os.Stdout), zapcore.DebugLevel)
			cores = append(cores, core)
		}
		if w == WriterFile {
			infoWrite := getLogWriterWithTime(cfg.LoggerFile, cfg.LogRollingPolicy, cfg.LogBackupCount)
			warnWrite := getLogWriterWithTime(cfg.LoggerWarnFile, cfg.LogRollingPolicy, cfg.LogBackupCount)
			errorWrite := getLogWriterWithTime(cfg.LoggerErrorFile, cfg.LogRollingPolicy, cfg.LogBackupCount)

			infoLevel := zap.LevelEnablerFunc(func(lvl zapcore.Level) bool {
				return lvl <= zapcore.InfoLevel
			})
			warnLevel := zap.LevelEnablerFunc(func(lvl zapcore.Level) bool {
				return lvl == zapcore.WarnLevel
			})
			errorLevel := zap.LevelEnablerFunc(func(lvl zapcore.Level) bool {
				return lvl >= zapcore.ErrorLevel
			})

			cores = append(cores, zapcore.NewCore(encoder, zapcore.AddSync(infoWrite), infoLevel))
			options = append(options, zap.AddStacktrace(zapcore.WarnLevel))
			cores = append(cores, zapcore.NewCore(encoder, zapcore.AddSync(warnWrite), warnLevel))
			cores = append(cores, zapcore.NewCore(encoder, zapcore.AddSync(errorWrite), errorLevel))
		}
	}

	combinedCore := zapcore.NewTee(cores...)

	options = append(options, zap.AddCaller())
	options = append(options, zap.AddCallerSkip(2))

	logger := zap.New(combinedCore, options...).Sugar()
	return &zapLogger{sugaredLogger: logger}, nil
}

func getJSONEncoder() zapcore.Encoder {
	return zapcore.NewJSONEncoder(zapcore.EncoderConfig{
		MessageKey:     "msg",
		LevelKey:       "level",
		TimeKey:        "time",
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		NameKey:        "app",
		CallerKey:      "file",
		StacktraceKey:  "trace",
		EncodeCaller:   zapcore.ShortCallerEncoder,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeDuration: zapcore.MillisDurationEncoder,
	})
}

func getConsoleEncoder() zapcore.Encoder {
	return zapcore.NewConsoleEncoder(zapcore.EncoderConfig{
		MessageKey:     "msg",
		LevelKey:       "level",
		TimeKey:        "time",
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		CallerKey:      "file",
		StacktraceKey:  "trace",
		EncodeCaller:   zapcore.ShortCallerEncoder,
		EncodeLevel:    zapcore.CapitalColorLevelEncoder,
		EncodeDuration: zapcore.MillisDurationEncoder,
	})
}

func getLogWriterWithTime(filename string, rotationPolicy string, backupCount uint) io.Writer {
	rotateDuration := time.Hour * 24
	if rotationPolicy == RotateTimeHourly {
		rotateDuration = time.Hour
	}
	hook, err := rotatelogs.New(
		filename+".%Y%m%d%H",
		rotatelogs.WithLinkName(filename),
		rotatelogs.WithRotationCount(backupCount),
		rotatelogs.WithRotationTime(rotateDuration),
	)
	if err != nil {
		panic(err)
	}
	return hook
}

func (l *zapLogger) Debug(args ...interface{})            { l.sugaredLogger.Debug(args...) }
func (l *zapLogger) Info(args ...interface{})             { l.sugaredLogger.Info(args...) }
func (l *zapLogger) Warn(args ...interface{})             { l.sugaredLogger.Warn(args...) }
func (l *zapLogger) Error(args ...interface{})            { l.sugaredLogger.Error(args...) }
func (l *zapLogger) Fatal(args ...interface{})            { l.sugaredLogger.Fatal(args...) }
func (l *zapLogger) Debugf(format string, args ...interface{}) { l.sugaredLogger.Debugf(format, args...) }
func (l *zapLogger) Infof(format string, args ...interface{})  { l.sugaredLogger.Infof(format, args...) }
func (l *zapLogger) Warnf(format string, args ...interface{})  { l.sugaredLogger.Warnf(format, args...) }
func (l *zapLogger) Errorf(format string, args ...interface{}) { l.sugaredLogger.Errorf(format, args...) }
func (l *zapLogger) Fatalf(format string, args ...interface{}) { l.sugaredLogger.Fatalf(format, args...) }
func (l *zapLogger) WithFields(fields Fields) Logger {
	var f = make([]interface{}, 0)
	for k, v := range fields {
		f = append(f, k)
		f = append(f, v)
	}
	return &zapLogger{l.sugaredLogger.With(f...)}
}
