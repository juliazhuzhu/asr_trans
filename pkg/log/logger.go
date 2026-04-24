package log

import "errors"

var log Logger

type Fields map[string]interface{}

const (
	InstanceZapLogger int = iota
)

var (
	errInvalidLoggerInstance = errors.New("invalid logger instance")
)

type Logger interface {
	Debug(args ...interface{})
	Info(args ...interface{})
	Warn(args ...interface{})
	Error(args ...interface{})
	Fatal(args ...interface{})
	Debugf(format string, args ...interface{})
	Infof(format string, args ...interface{})
	Warnf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
	Fatalf(format string, args ...interface{})
	WithFields(keyValues Fields) Logger
}

type Config struct {
	Writers          string
	LoggerLevel      string
	LoggerFile       string
	LoggerWarnFile   string
	LoggerErrorFile  string
	LogFormatText    bool
	LogRollingPolicy string
	LogRotateDate    int
	LogRotateSize    int
	LogBackupCount   uint
	AppName          string
}

func NewLogger(cfg *Config, loggerInstance int) error {
	switch loggerInstance {
	case InstanceZapLogger:
		logger, err := newZapLogger(cfg)
		if err != nil {
			return err
		}
		log = logger
		return nil
	default:
		return errInvalidLoggerInstance
	}
}

func Debug(args ...interface{})            { log.Debug(args...) }
func Info(args ...interface{})             { log.Info(args...) }
func Warn(args ...interface{})             { log.Warn(args...) }
func Error(args ...interface{})            { log.Error(args...) }
func Fatal(args ...interface{})            { log.Fatal(args...) }
func Debugf(format string, args ...interface{}) { log.Debugf(format, args...) }
func Infof(format string, args ...interface{})  { log.Infof(format, args...) }
func Warnf(format string, args ...interface{})  { log.Warnf(format, args...) }
func Errorf(format string, args ...interface{}) { log.Errorf(format, args...) }
func Fatalf(format string, args ...interface{}) { log.Fatalf(format, args...) }
func WithFields(keyValues Fields) Logger        { return log.WithFields(keyValues) }
