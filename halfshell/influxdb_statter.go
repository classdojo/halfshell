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
	"fmt"
	"net/http"
  "net/url"
	"os"
	"time"
  influx "github.com/influxdb/influxdb/client/v2"
)

type influxStatter struct {
  metricsChannel chan influx.Point
  client         influx.Client
  hostname       string
  prefix         string
  tags           map[string]string
	Name           string
	Logger         *Logger
	Enabled        bool
}

func newInfluxStatterWithConfig(routeConfig *RouteConfig, statterConfig *StatterConfig) Statter {
	logger := NewLogger("stats.%s", routeConfig.Name)
	hostname, _ := os.Hostname()

  url, err := url.Parse(fmt.Sprintf("%s:%d", statterConfig.Host, statterConfig.Port))
  if err != nil {
    logger.Errorf("Unable to parse influxDB url: %v", err)
    return nil
  }

  influxConfig := influx.Config{
    URL: url,
    Username: statterConfig.Username,
    Password: statterConfig.Password,
  }

  client := influx.NewClient(influxConfig)

  metricsChannel := make(chan influx.Point)
  batch := newBatchPointsForDB(statterConfig.Database, logger)
  interval := time.Duration(statterConfig.Interval)*time.Second
  ticker := time.NewTicker(interval)

  go func() {
    for {
      select {
      case point := <-metricsChannel:
        batch.AddPoint(&point)
      case <-ticker.C:
        //  send current batch to influx
        err := client.Write(batch)
        if err != nil {
          logger.Errorf("Unable to write batch to influx db: %v", err)
        }
        // start a new batch
        batch = newBatchPointsForDB(statterConfig.Database, logger)
      }
    }
  }()

  return &influxStatter{
    metricsChannel: metricsChannel,
    client: client,
    Name: routeConfig.Name,
    Logger: logger,
    Enabled: statterConfig.Enabled,
    hostname: hostname,
    prefix: statterConfig.Prefix,
    tags: statterConfig.Tags,
  }
}

func (s *influxStatter) RegisterRequest(w *ResponseWriter, r *Request) {
	if !s.Enabled {
		return
	}

	now := time.Now()

	status := "success"
	if w.Status != http.StatusOK {
		status = "failure"
	}

  dimensions := r.ProcessorOptions.Dimensions
  tags := map[string]string{
    "hostname": s.hostname,
    "route": s.Name,
    "width": fmt.Sprintf("%d", dimensions.Width),
    "height": fmt.Sprintf("%d", dimensions.Height),
  }
  for k, v := range s.tags {
    tags[k] = v
  }

	s.count(fmt.Sprintf("http.status.%d", w.Status), tags)
	s.count(fmt.Sprintf("image_resized.%s", status), tags)

	if status == "success" {
		durationInMs := (now.UnixNano() - r.Timestamp.UnixNano()) / 1000000
		s.time("image_resized", durationInMs, tags)
	}
}

func (s *influxStatter) count(stat string, tags map[string]string) {
	stat = fmt.Sprintf("%s.%s.%s", s.prefix, s.Name, stat)
	s.Logger.Infof("Incrementing counter: %s", stat)
  fields := map[string]interface{}{"count": 1}
  s.enqueue(stat, tags, fields)
}

func (s *influxStatter) time(stat string, time int64, tags map[string]string) {
	stat = fmt.Sprintf("%s.%s.%s", s.prefix, s.Name, stat)
	s.Logger.Infof("Registering time: %s (%d)", stat, time)
  fields := map[string]interface{}{"duration": time}
  s.enqueue(stat, tags, fields)
}

func (s *influxStatter) enqueue(name string, tags map[string]string, fields map[string]interface{}) {
  point, err := influx.NewPoint(name, tags, fields, time.Now())
  if err != nil {
    s.Logger.Errorf("Error creating new metrics point: %v", err)
  }
  s.metricsChannel <- *point
}

func newBatchPointsForDB(database string, logger *Logger) influx.BatchPoints {
  batch, err := influx.NewBatchPoints(influx.BatchPointsConfig{
    Database: database,
  })
  if err != nil {
    logger.Errorf("Unable to create points batch: %v", err)
    return nil
  }
  return batch
}
