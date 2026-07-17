package exporter

import (
	"fmt"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

/* ================ Logger ================ */

func configureLogger(levelStr, formatStr string) *slog.Logger {
	var level slog.Level
	switch strings.ToLower(levelStr) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo // fallback to default info level
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}

	var handler slog.Handler
	switch strings.ToLower(formatStr) {
	case "json":
		handler = slog.NewJSONHandler(os.Stderr, opts)
	case "logfmt", "":
		handler = slog.NewTextHandler(os.Stderr, opts)
	default:
		// Be resilient to misconfiguration: fall back to logfmt.
		handler = slog.NewTextHandler(os.Stderr, opts)
	}

	return slog.New(handler)
}

func loggerOrDefault() *slog.Logger {
	if Logger != nil {
		return Logger
	}
	return slog.Default()
}

// logDebugf will log debug message
func logDebugf(format string, v ...interface{}) {
	loggerOrDefault().Debug(fmt.Sprintf(format, v...))
}

// logInfof will log info message
func logInfof(format string, v ...interface{}) {
	loggerOrDefault().Info(fmt.Sprintf(format, v...))
}

// logWarnf will log warning message
func logWarnf(format string, v ...interface{}) {
	loggerOrDefault().Warn(fmt.Sprintf(format, v...))
}

// logErrorf will log error message
func logErrorf(format string, v ...interface{}) {
	loggerOrDefault().Error(fmt.Sprintf(format, v...))
}

// logError will print error message directly
func logError(msg string) {
	loggerOrDefault().Error(msg)
}

// logFatalf will log error message
func logFatalf(format string, v ...interface{}) {
	loggerOrDefault().Error(fmt.Sprintf(format, v...))
	os.Exit(1)
}

/* ================ Auxiliaries ================ */

// castFloat64 will cast datum into float64 with Column scale & default value.
// Column.Scale/Column.Default are parsed when loading config, so this is hot-path safe.
func castFloat64(t interface{}, c *Column) float64 {
	scale := 1.0
	if c != nil && c.hasScale {
		scale = c.scaleFactor
	}

	switch v := t.(type) {
	case int64:
		return float64(v) * scale
	case float64:
		return v * scale
	case time.Time:
		return float64(v.Unix())
	case []byte:
		strV := string(v)
		result, err := strconv.ParseFloat(strV, 64)
		if err != nil {
			logWarnf("fail casting []byte to float64: %v", t)
			return math.NaN()
		}
		return result * scale
	case string:
		result, err := strconv.ParseFloat(v, 64)
		if err != nil {
			logWarnf("fail casting string to float64: %v", t)
			return math.NaN()
		}
		return result * scale
	case bool:
		if v {
			return 1.0
		}
		return 0.0
	case nil:
		if c != nil && c.hasDefault {
			return c.defaultValue * scale
		}
		return math.NaN()
	default:
		logWarnf("fail casting unknown to float64: %v", t)
		return math.NaN()
	}
}

// castHistogramFloat64 converts one raw SQL histogram observation. A NULL is
// absent unless the column explicitly defines a default. Histogram samples are
// required to be finite because one invalid observation invalidates the whole
// snapshot rather than publishing a partial distribution.
func castHistogramFloat64(t interface{}, c *Column) (value float64, present bool, err error) {
	if t == nil && (c == nil || !c.hasDefault) {
		return 0, false, nil
	}

	scale := 1.0
	if c != nil && c.hasScale {
		scale = c.scaleFactor
	}
	if t == nil {
		t = c.defaultValue
	}

	// scale semantics must mirror castFloat64: epoch timestamps and booleans
	// are never scaled, so a column yields the same value under any usage
	switch v := t.(type) {
	case int64:
		value = float64(v) * scale
	case float64:
		value = v * scale
	case time.Time:
		value = float64(v.Unix())
	case []byte:
		value, err = strconv.ParseFloat(string(v), 64)
		value *= scale
	case string:
		value, err = strconv.ParseFloat(v, 64)
		value *= scale
	case bool:
		if v {
			value = 1
		}
	default:
		err = fmt.Errorf("cannot cast %T to float64", t)
	}
	if err != nil {
		return 0, false, err
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false, fmt.Errorf("non-finite observation %v", value)
	}
	return value, true, nil
}

// encodeLabelTuple returns an unambiguous map key for an ordered label tuple.
// Length-prefixing avoids collisions such as ["a", "bc"] and ["ab", "c"].
func encodeLabelTuple(labels []string) string {
	var b strings.Builder
	for _, label := range labels {
		b.WriteString(strconv.Itoa(len(label)))
		b.WriteByte(':')
		b.WriteString(label)
	}
	return b.String()
}

// castString will force interface{} into string
func castString(t interface{}) string {
	switch v := t.(type) {
	case int64:
		return fmt.Sprintf("%v", v)
	case float64:
		return fmt.Sprintf("%v", v)
	case time.Time:
		return fmt.Sprintf("%v", v.Unix())
	case nil:
		return ""
	case []byte:
		// Try and convert to string
		return string(v)
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		logWarnf("fail casting unknown to string: %v", t)
		return ""
	}
}

// parseConstLabels turn param string into prometheus.Labels
func parseConstLabels(s string) prometheus.Labels {
	labels := make(prometheus.Labels)
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return nil
	}

	parts := strings.Split(s, ",")
	for _, p := range parts {
		keyValue := strings.SplitN(strings.TrimSpace(p), "=", 2)
		if len(keyValue) != 2 {
			logErrorf(`malformed labels format %q, should be "key=value"`, p)
			continue
		}
		key := strings.TrimSpace(keyValue[0])
		value := strings.TrimSpace(keyValue[1])
		if key == "" || value == "" {
			continue
		}
		if err := validatePromLabelName(key); err != nil {
			logWarnf("skip invalid const label name %q: %v", key, err)
			continue
		}
		labels[key] = value
	}
	if len(labels) == 0 {
		return nil
	}

	return labels
}

// parseCSV will turn a comma separated string into a []string
func parseCSV(s string) (tags []string) {
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return nil
	}

	parts := strings.Split(s, ",")
	for _, p := range parts {
		if tag := strings.TrimSpace(p); len(tag) > 0 {
			tags = append(tags, tag)
		}
	}

	if len(tags) == 0 {
		return nil
	}
	return
}
