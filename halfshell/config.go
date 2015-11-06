// Copyright (c) 2014 Oyster
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package halfshell

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
)

// Config is the primary configuration of Halfshell. It contains the server
// configuration as well as a list of route configurations.
type Config struct {
	ServerConfig  *ServerConfig
	StatterConfig *StatterConfig
	RouteConfigs  []*RouteConfig
}

// ServerConfig holds the configuration settings relevant for the HTTP server.
type ServerConfig struct {
	Port         uint64
	ReadTimeout  uint64
	WriteTimeout uint64
}

// RouteConfig holds the configuration settings for a particular route.
type RouteConfig struct {
	Name            string
	CacheControl    string
	Pattern         *regexp.Regexp
	ImagePathIndex  int
	SourceConfig    *SourceConfig
	ProcessorConfig *ProcessorConfig
}

// SourceConfig holds the type information and configuration settings for a
// particular image source.
type SourceConfig struct {
	Name        string
	Type        ImageSourceType
	S3AccessKey string
	S3Bucket    string
	S3SecretKey string
	Directory   string
	Host        string
}

// ProcessorConfig holds the configuration settings for the image processor.
type ProcessorConfig struct {
	Name                    string
	ImageCompressionQuality uint64
	DefaultScaleMode        uint
	DefaultImageHeight      uint64
	DefaultImageWidth       uint64
	MaxImageDimensions      ImageDimensions
	MaxBlurRadiusPercentage float64
	AutoOrient              bool
	Formats                 map[string]FormatConfig

	// DEPRECATED
	MaintainAspectRatio bool
}

type FormatConfig struct {
	Width  uint64
	Height uint64
	Blur   float64
}

// StatterConfig holds configuration data for StatsD or InfluxDB
type StatterConfig struct {
	Enabled  bool
	Host     string
	Port     uint64
	Username string
	Password string
	Database string
	Type     string            // "statsd" or "influxdb"
	Prefix   string            // string to be prepended to each metric title
	Tags     map[string]string // for influx only
	Interval uint              // batch send interval in seconds, for influx only
}

// NewConfigFromFile parses a JSON configuration file and returns a pointer to
// a new Config object.
func NewConfigFromFile(filepath string) *Config {
	parser := newConfigParser(filepath)
	config := parser.parse()
	return config
}

type configParser struct {
	filepath string
	data     map[string]interface{}
}

func newConfigParser(filepath string) *configParser {
	file, err := os.Open(filepath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to open file %s\n", filepath)
		os.Exit(1)
	}
	decoder := json.NewDecoder(file)
	parser := configParser{filepath: filepath}
	decoder.Decode(&parser.data)
	return &parser
}

func (c *configParser) parse() *Config {
	config := Config{
		ServerConfig:  c.parseServerConfig(),
		StatterConfig: c.parseStatterConfig(),
	}

	sourceConfigsByName := make(map[string]*SourceConfig)
	processorConfigsByName := make(map[string]*ProcessorConfig)

	for sourceName := range c.data["sources"].(map[string]interface{}) {
		sourceConfigsByName[sourceName] = c.parseSourceConfig(sourceName)
	}

	for processorName := range c.data["processors"].(map[string]interface{}) {
		processorConfigsByName[processorName] = c.parseProcessorConfig(processorName)
	}

	routesData := c.data["routes"].(map[string]interface{})
	for routePatternString := range routesData {
		routeConfig := &RouteConfig{ImagePathIndex: -1}
		routeData := routesData[routePatternString].(map[string]interface{})
		pattern, err := regexp.Compile(routePatternString)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid route pattern %s: %v\n", routePatternString, err)
			os.Exit(1)
		}

		for i, expName := range pattern.SubexpNames() {
			if expName == "image_path" {
				routeConfig.ImagePathIndex = i
			}
		}

		if routeConfig.ImagePathIndex == -1 {
			fmt.Fprintf(os.Stderr, "No 'image_path' named group in regex: %s\n", routePatternString)
			os.Exit(1)
		}

		processorKey := routeData["processor"].(string)
		sourceKey := routeData["source"].(string)

		routeConfig.Name = routeData["name"].(string)
		routeConfig.Pattern = pattern
		routeConfig.ProcessorConfig = processorConfigsByName[processorKey]
		routeConfig.SourceConfig = sourceConfigsByName[sourceKey]
		if _, ok := routeData["cache_control"]; ok {
			routeConfig.CacheControl = routeData["cache_control"].(string)
		}

		config.RouteConfigs = append(config.RouteConfigs, routeConfig)
	}

	return &config
}

func (c *configParser) parseServerConfig() *ServerConfig {
	return &ServerConfig{
		Port:         c.uintForKeypath("server.port"),
		ReadTimeout:  c.uintForKeypath("server.read_timeout"),
		WriteTimeout: c.uintForKeypath("server.write_timeout"),
	}
}

func (c *configParser) parseStatterConfig() *StatterConfig {
	statsd, statsdExists := c.data["statsd"].(map[string]interface{})
	influxdb, influxdbExists := c.data["influxdb"].(map[string]interface{})

	if statsdExists && influxdbExists {
		panic("Cannot enable both statsd and influxDB.")
	}

	var metricsType string
	var metrics map[string]interface{}
	var enabled interface{}
	var ok bool
	if influxdbExists {
		metrics = influxdb
		metricsType = "influxdb"
		enabled, ok = influxdb["enabled"]
		if !ok {
			enabled = true
		}
	} else if statsdExists {
		metrics = statsd
		metricsType = "statsd"
		enabled, ok = statsd["enabled"]
		if !ok {
			enabled = true
		}
	} else {
		metrics = statsd
		enabled = true
		metricsType = "statsd"
	}

	host, _ := metrics["host"].(string)
	if host == "" {
		host = "0"
	}

	port, _ := metrics["port"].(float64)
	if port == 0 {
		port = 8125
	}

	username, _ := metrics["username"].(string)
	password, _ := metrics["password"].(string)
	database, _ := metrics["database"].(string)

	prefix, _ := metrics["prefix"].(string)
	if prefix == "" {
		hostname, _ := os.Hostname()
		prefix = fmt.Sprintf("%s.halfshell", hostname)
	}

	rawTags := metrics["tags"].(map[string]interface{})
	tags := make(map[string]string)
	for k, v := range rawTags {
		tags[k] = v.(string)
	}

	interval, _ := metrics["interval"].(uint)
	if interval == 0 {
		interval = 10
	}

	return &StatterConfig{
		Host:     host,
		Port:     uint64(port),
		Enabled:  enabled.(bool),
		Username: username,
		Password: password,
		Database: database,
		Type:     metricsType,
		Prefix:   prefix,
		Tags:     tags,
		Interval: interval,
	}
}

func (c *configParser) parseSourceConfig(sourceName string) *SourceConfig {
	return &SourceConfig{
		Name:        sourceName,
		Type:        ImageSourceType(c.stringForKeypath("sources.%s.type", sourceName)),
		S3AccessKey: c.stringForKeypath("sources.%s.s3_access_key", sourceName),
		S3SecretKey: c.stringForKeypath("sources.%s.s3_secret_key", sourceName),
		S3Bucket:    c.stringForKeypath("sources.%s.s3_bucket", sourceName),
		Directory:   c.stringForKeypath("sources.%s.directory", sourceName),
		Host:        c.stringForKeypath("sources.%s.host", sourceName),
	}
}

func (c *configParser) parseProcessorConfig(processorName string) *ProcessorConfig {
	scaleModeName := c.stringForKeypath("processors.%s.default_scale_mode", processorName)
	scaleMode, _ := ScaleModes[scaleModeName]
	if scaleMode == 0 {
		scaleMode = ScaleFill
	}

	maxDimensions := ImageDimensions{
		Width:  uint(c.uintForKeypath("processors.%s.max_image_width", processorName)),
		Height: uint(c.uintForKeypath("processors.%s.max_image_height", processorName)),
	}

	formats := make(map[string]FormatConfig)
	processor := c.data["processors"].(map[string]interface{})[processorName].(map[string]interface{})
	if _, ok := processor["formats"]; ok {
		for formatName := range processor["formats"].(map[string]interface{}) {
			format := FormatConfig{
				Width:  c.uintForKeypath("processors.%s.formats.%s.width", processorName, formatName),
				Height: c.uintForKeypath("processors.%s.formats.%s.height", processorName, formatName),
				Blur:   c.floatForKeypath("processors.%s.formats.%s.blur", processorName, formatName),
			}
			formats[formatName] = format
		}
	}

	config := &ProcessorConfig{
		Name: processorName,
		ImageCompressionQuality: c.uintForKeypath("processors.%s.image_compression_quality", processorName),
		DefaultScaleMode:        scaleMode,
		DefaultImageHeight:      c.uintForKeypath("processors.%s.default_image_height", processorName),
		DefaultImageWidth:       c.uintForKeypath("processors.%s.default_image_width", processorName),
		MaxImageDimensions:      maxDimensions,
		MaxBlurRadiusPercentage: c.floatForKeypath("processors.%s.max_blur_radius_percentage", processorName),
		AutoOrient:              c.boolForKeypath("processors.%s.auto_orient", processorName),
		Formats:                 formats,

		// DEPRECATED
		MaintainAspectRatio: c.boolForKeypath("processors.%s.maintain_aspect_ratio", processorName),
	}

	if config.MaintainAspectRatio {
		config.DefaultScaleMode = ScaleAspectFit
	}

	return config
}

func (c *configParser) valueForKeypath(valueType reflect.Kind, keypathFormat string, v ...interface{}) interface{} {
	keypath := fmt.Sprintf(keypathFormat, v...)
	components := strings.Split(keypath, ".")
	var currentData = c.data
	for _, component := range components[:len(components)-1] {
		currentData = currentData[component].(map[string]interface{})
	}
	value := currentData[components[len(components)-1]]
	if value == nil && len(v) > 0 {
		return c.valueForKeypath(valueType, fmt.Sprintf(keypathFormat, "default"))
	}

	switch value.(type) {
	case string, bool, float64:
		return value
	case nil:
		switch valueType {
		case reflect.Float64:
			return float64(0)
		case reflect.String:
			return ""
		case reflect.Bool:
			return false
		default:
			panic("Unreachable")
		}
	default:
		panic("Unreachable")
	}
}

func (c *configParser) stringForKeypath(keypathFormat string, v ...interface{}) string {
	return c.valueForKeypath(reflect.String, keypathFormat, v...).(string)
}

func (c *configParser) floatForKeypath(keypathFormat string, v ...interface{}) float64 {
	return c.valueForKeypath(reflect.Float64, keypathFormat, v...).(float64)
}

func (c *configParser) uintForKeypath(keypathFormat string, v ...interface{}) uint64 {
	return uint64(c.floatForKeypath(keypathFormat, v...))
}

func (c *configParser) boolForKeypath(keypathFormat string, v ...interface{}) bool {
	return c.valueForKeypath(reflect.Bool, keypathFormat, v...).(bool)
}
