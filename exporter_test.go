// Copyright 2016-2017 Percona LLC
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/smartystreets/goconvey/convey"
	"gopkg.in/DATA-DOG/go-sqlmock.v1"
)

type labelMap map[string]string

type metricResult struct {
	name       string
	labels     labelMap
	value      float64
	metricType dto.MetricType
}

var nameRE = regexp.MustCompile(`fqName: "(\w+)"`)

func getName(d *prometheus.Desc) string {
	m := nameRE.FindStringSubmatch(d.String())
	if len(m) != 2 {
		panic("failed to get metric name from " + d.String())
	}
	return m[1]
}

func readMetric(m prometheus.Metric) *metricResult {
	pb := &dto.Metric{}
	err := m.Write(pb)
	if err != nil {
		panic(err)
	}

	name := getName(m.Desc())
	labels := make(labelMap, len(pb.Label))
	for _, v := range pb.Label {
		labels[v.GetName()] = v.GetValue()
	}
	if pb.Gauge != nil {
		return &metricResult{name, labels, pb.GetGauge().GetValue(), dto.MetricType_GAUGE}
	}
	if pb.Counter != nil {
		return &metricResult{name, labels, pb.GetCounter().GetValue(), dto.MetricType_COUNTER}
	}
	if pb.Untyped != nil {
		return &metricResult{name, labels, pb.GetUntyped().GetValue(), dto.MetricType_UNTYPED}
	}
	panic("Unsupported metric type")
}

func sanitizeQuery(q string) string {
	q = strings.Join(strings.Fields(q), " ")
	q = strings.Replace(q, "(", "\\(", -1)
	q = strings.Replace(q, ")", "\\)", -1)
	q = strings.Replace(q, "*", "\\*", -1)
	return q
}

func TestScrapeMySQLGlobal(t *testing.T) {
	convey.Convey("Metrics are lowercase", t, convey.FailureContinues, func() {
		for c, m := range mySQLGlobalMetrics {
			convey.So(c, convey.ShouldEqual, strings.ToLower(c))
			convey.So(m.name, convey.ShouldEqual, strings.ToLower(m.name))
		}
	})

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("error opening a stub database connection: %s", err)
	}
	defer db.Close()

	columns := []string{"Variable_Name", "Variable_Value"}
	rows := sqlmock.NewRows(columns).
		AddRow("Active_Transactions", "3").
		AddRow("Backend_query_time_nsec", "76355784684851").
		AddRow("Client_Connections_aborted", "0").
		AddRow("Client_Connections_connected", "64").
		AddRow("Client_Connections_created", "1087931").
		AddRow("Servers_table_version", "2019470")
	mock.ExpectQuery(mySQLGlobalQuery).WillReturnRows(rows)

	ch := make(chan prometheus.Metric)
	go func() {
		if err = scrapeMySQLGlobal(db, ch); err != nil {
			t.Errorf("error calling function on test: %s", err)
		}
		close(ch)
	}()

	counterExpected := []*metricResult{
		{"proxysql_mysql_status_active_transactions", labelMap{}, 3, dto.MetricType_GAUGE},
		{"proxysql_mysql_status_backend_query_time_nsec", labelMap{}, 76355784684851, dto.MetricType_UNTYPED},
		{"proxysql_mysql_status_client_connections_aborted", labelMap{}, 0, dto.MetricType_COUNTER},
		{"proxysql_mysql_status_client_connections_connected", labelMap{}, 64, dto.MetricType_GAUGE},
		{"proxysql_mysql_status_client_connections_created", labelMap{}, 1087931, dto.MetricType_COUNTER},
		{"proxysql_mysql_status_servers_table_version", labelMap{}, 2019470, dto.MetricType_UNTYPED},
	}
	convey.Convey("Metrics comparison", t, convey.FailureContinues, func() {
		for _, expect := range counterExpected {
			got := readMetric(<-ch)
			convey.So(got, convey.ShouldResemble, expect)
		}
	})

	// Ensure all SQL queries were executed
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("there were unfulfilled expectations: %s", err)
	}
}

func TestScrapeMySQLConnectionPool(t *testing.T) {
	convey.Convey("Metrics are lowercase", t, convey.FailureContinues, func() {
		for c, m := range mySQLconnectionPoolMetrics {
			convey.So(c, convey.ShouldEqual, strings.ToLower(c))
			convey.So(m.name, convey.ShouldEqual, strings.ToLower(m.name))
		}
	})

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("error opening a stub database connection: %s", err)
	}
	defer db.Close()

	columns := []string{"hostgroup", "srv_host", "srv_port", "status", "ConnUsed", "ConnFree", "ConnOK", "ConnERR",
		"Queries", "Bytes_data_sent", "Bytes_data_recv", "Latency_ms"}
	rows := sqlmock.NewRows(columns).
		AddRow("0", "10.91.142.80", "3306", "ONLINE", "0", "45", "1895677", "46", "197941647", "10984550806", "321063484988", "163").
		AddRow("0", "10.91.142.82", "3306", "SHUNNED", "0", "97", "39859", "0", "386686994", "21643682247", "641406745151", "255").
		AddRow("1", "10.91.142.88", "3306", "OFFLINE_SOFT", "0", "18", "31471", "6391", "255993467", "14327840185", "420795691329", "283").
		AddRow("2", "10.91.142.89", "3306", "OFFLINE_HARD", "0", "18", "31471", "6391", "255993467", "14327840185", "420795691329", "283")
	mock.ExpectQuery(sanitizeQuery(mySQLconnectionPoolQuery)).WillReturnRows(rows)

	ch := make(chan prometheus.Metric)
	go func() {
		if err = scrapeMySQLConnectionPool(db, ch); err != nil {
			t.Errorf("error calling function on test: %s", err)
		}
		close(ch)
	}()

	counterExpected := []*metricResult{
		{"proxysql_connection_pool_status", labelMap{"hostgroup": "0", "endpoint": "10.91.142.80:3306"}, 1, dto.MetricType_GAUGE},
		{"proxysql_connection_pool_conn_used", labelMap{"hostgroup": "0", "endpoint": "10.91.142.80:3306"}, 0, dto.MetricType_GAUGE},
		{"proxysql_connection_pool_conn_free", labelMap{"hostgroup": "0", "endpoint": "10.91.142.80:3306"}, 45, dto.MetricType_GAUGE},
		{"proxysql_connection_pool_conn_ok", labelMap{"hostgroup": "0", "endpoint": "10.91.142.80:3306"}, 1895677, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_conn_err", labelMap{"hostgroup": "0", "endpoint": "10.91.142.80:3306"}, 46, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_queries", labelMap{"hostgroup": "0", "endpoint": "10.91.142.80:3306"}, 197941647, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_bytes_data_sent", labelMap{"hostgroup": "0", "endpoint": "10.91.142.80:3306"}, 10984550806, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_bytes_data_recv", labelMap{"hostgroup": "0", "endpoint": "10.91.142.80:3306"}, 321063484988, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_latency_us", labelMap{"hostgroup": "0", "endpoint": "10.91.142.80:3306"}, 163, dto.MetricType_GAUGE},

		{"proxysql_connection_pool_status", labelMap{"hostgroup": "0", "endpoint": "10.91.142.82:3306"}, 2, dto.MetricType_GAUGE},
		{"proxysql_connection_pool_conn_used", labelMap{"hostgroup": "0", "endpoint": "10.91.142.82:3306"}, 0, dto.MetricType_GAUGE},
		{"proxysql_connection_pool_conn_free", labelMap{"hostgroup": "0", "endpoint": "10.91.142.82:3306"}, 97, dto.MetricType_GAUGE},
		{"proxysql_connection_pool_conn_ok", labelMap{"hostgroup": "0", "endpoint": "10.91.142.82:3306"}, 39859, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_conn_err", labelMap{"hostgroup": "0", "endpoint": "10.91.142.82:3306"}, 0, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_queries", labelMap{"hostgroup": "0", "endpoint": "10.91.142.82:3306"}, 386686994, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_bytes_data_sent", labelMap{"hostgroup": "0", "endpoint": "10.91.142.82:3306"}, 21643682247, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_bytes_data_recv", labelMap{"hostgroup": "0", "endpoint": "10.91.142.82:3306"}, 641406745151, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_latency_us", labelMap{"hostgroup": "0", "endpoint": "10.91.142.82:3306"}, 255, dto.MetricType_GAUGE},

		{"proxysql_connection_pool_status", labelMap{"hostgroup": "1", "endpoint": "10.91.142.88:3306"}, 3, dto.MetricType_GAUGE},
		{"proxysql_connection_pool_conn_used", labelMap{"hostgroup": "1", "endpoint": "10.91.142.88:3306"}, 0, dto.MetricType_GAUGE},
		{"proxysql_connection_pool_conn_free", labelMap{"hostgroup": "1", "endpoint": "10.91.142.88:3306"}, 18, dto.MetricType_GAUGE},
		{"proxysql_connection_pool_conn_ok", labelMap{"hostgroup": "1", "endpoint": "10.91.142.88:3306"}, 31471, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_conn_err", labelMap{"hostgroup": "1", "endpoint": "10.91.142.88:3306"}, 6391, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_queries", labelMap{"hostgroup": "1", "endpoint": "10.91.142.88:3306"}, 255993467, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_bytes_data_sent", labelMap{"hostgroup": "1", "endpoint": "10.91.142.88:3306"}, 14327840185, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_bytes_data_recv", labelMap{"hostgroup": "1", "endpoint": "10.91.142.88:3306"}, 420795691329, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_latency_us", labelMap{"hostgroup": "1", "endpoint": "10.91.142.88:3306"}, 283, dto.MetricType_GAUGE},

		{"proxysql_connection_pool_status", labelMap{"hostgroup": "2", "endpoint": "10.91.142.89:3306"}, 4, dto.MetricType_GAUGE},
		{"proxysql_connection_pool_conn_used", labelMap{"hostgroup": "2", "endpoint": "10.91.142.89:3306"}, 0, dto.MetricType_GAUGE},
		{"proxysql_connection_pool_conn_free", labelMap{"hostgroup": "2", "endpoint": "10.91.142.89:3306"}, 18, dto.MetricType_GAUGE},
		{"proxysql_connection_pool_conn_ok", labelMap{"hostgroup": "2", "endpoint": "10.91.142.89:3306"}, 31471, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_conn_err", labelMap{"hostgroup": "2", "endpoint": "10.91.142.89:3306"}, 6391, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_queries", labelMap{"hostgroup": "2", "endpoint": "10.91.142.89:3306"}, 255993467, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_bytes_data_sent", labelMap{"hostgroup": "2", "endpoint": "10.91.142.89:3306"}, 14327840185, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_bytes_data_recv", labelMap{"hostgroup": "2", "endpoint": "10.91.142.89:3306"}, 420795691329, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_latency_us", labelMap{"hostgroup": "2", "endpoint": "10.91.142.89:3306"}, 283, dto.MetricType_GAUGE},
	}
	convey.Convey("Metrics comparison", t, convey.FailureContinues, func() {
		for _, expect := range counterExpected {
			got := readMetric(<-ch)
			convey.So(got, convey.ShouldResemble, expect)
		}
	})

	// Ensure all SQL queries were executed
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("there were unfulfilled expectations: %s", err)
	}
}