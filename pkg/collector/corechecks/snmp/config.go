package snmp

import (
	"fmt"
	"github.com/DataDog/datadog-agent/pkg/autodiscovery/integration"
	"github.com/DataDog/datadog-agent/pkg/util/log"
	"gopkg.in/yaml.v2"
	"path/filepath"
)

var defaultOidBatchSize = 60
var defaultPort = uint16(161)
var defaultRetries = 3
var defaultTimeout = 2

type snmpInitConfig struct {
	Profiles      profileConfigMap `yaml:"profiles"`
	GlobalMetrics []metricsConfig  `yaml:"global_metrics"`
}

type snmpInstanceConfig struct {
	IPAddress        string            `yaml:"ip_address"`
	Port             Number            `yaml:"port"`
	CommunityString  string            `yaml:"community_string"`
	SnmpVersion      string            `yaml:"snmp_version"`
	Timeout          Number            `yaml:"timeout"`
	Retries          Number            `yaml:"retries"`
	User             string            `yaml:"user"`
	AuthProtocol     string            `yaml:"authProtocol"`
	AuthKey          string            `yaml:"authKey"`
	PrivProtocol     string            `yaml:"privProtocol"`
	PrivKey          string            `yaml:"privKey"`
	ContextName      string            `yaml:"context_name"`
	Metrics          []metricsConfig   `yaml:"metrics"`
	MetricTags       []metricTagConfig `yaml:"metric_tags"`
	Profile          string            `yaml:"profile"`
	UseGlobalMetrics bool              `yaml:"use_global_metrics"`
}

type snmpConfig struct {
	ipAddress         string
	port              uint16
	communityString   string
	snmpVersion       string
	timeout           int
	retries           int
	user              string
	authProtocol      string
	authKey           string
	privProtocol      string
	privKey           string
	contextName       string
	oidConfig         oidConfig
	metrics           []metricsConfig
	metricTags        []metricTagConfig
	oidBatchSize      int
	profiles          profileDefinitionMap
	profileTags       []string
	uptimeMetricAdded bool
}

func (c *snmpConfig) refreshWithProfile(profile string) error {
	if _, ok := c.profiles[profile]; !ok {
		return fmt.Errorf("unknown profile `%s`", profile)
	}
	log.Debugf("Refreshing with profile `%s` with content: %#v", profile, c.profiles[profile])
	tags := []string{"snmp_profile:" + profile}
	definition := c.profiles[profile]

	c.metrics = append(c.metrics, definition.Metrics...)
	c.metricTags = append(c.metricTags, definition.MetricTags...)
	c.oidConfig.scalarOids = append(c.oidConfig.scalarOids, parseScalarOids(definition.Metrics, definition.MetricTags)...)
	c.oidConfig.columnOids = append(c.oidConfig.columnOids, parseColumnOids(definition.Metrics)...)

	if definition.Device.Vendor != "" {
		tags = append(tags, "device_vendor:"+definition.Device.Vendor)
	}
	c.profileTags = tags
	return nil
}

func (c *snmpConfig) addUptimeMetric() {
	if c.uptimeMetricAdded {
		return
	}
	metricConfig := getUptimeMetricConfig()
	c.metrics = append(c.metrics, metricConfig)
	c.oidConfig.scalarOids = append(c.oidConfig.scalarOids, metricConfig.Symbol.OID)
	c.uptimeMetricAdded = true
}

func (c *snmpConfig) getStaticTags() []string {
	tags := []string{"snmp_device:" + c.ipAddress}
	return tags
}

// toString used for logging, it will hide sensitive information
func (c *snmpConfig) toString() string {
	configCopy := *c
	configCopy.communityString = "***"
	configCopy.authKey = "***"
	configCopy.privKey = "***"
	return fmt.Sprintf("%#v", configCopy)
}

func buildConfig(rawInstance integration.Data, rawInitConfig integration.Data) (snmpConfig, error) {
	instance := snmpInstanceConfig{}
	initConfig := snmpInitConfig{}

	// Set defaults before unmarshalling
	instance.UseGlobalMetrics = true

	err := yaml.Unmarshal(rawInitConfig, &initConfig)
	if err != nil {
		return snmpConfig{}, err
	}

	err = yaml.Unmarshal(rawInstance, &instance)
	if err != nil {
		return snmpConfig{}, err
	}

	c := snmpConfig{}

	c.snmpVersion = instance.SnmpVersion
	c.ipAddress = instance.IPAddress
	c.port = uint16(instance.Port)

	if c.port == 0 {
		c.port = defaultPort
	}

	if instance.Retries == 0 {
		c.retries = defaultRetries
	} else {
		c.retries = int(instance.Retries)
	}

	if instance.Timeout == 0 {
		c.timeout = defaultTimeout
	} else {
		c.timeout = int(instance.Timeout)
	}

	// SNMP connection configs
	c.communityString = instance.CommunityString
	c.user = instance.User
	c.authProtocol = instance.AuthProtocol
	c.authKey = instance.AuthKey
	c.privProtocol = instance.PrivProtocol
	c.privKey = instance.PrivKey
	c.contextName = instance.ContextName

	c.metrics = instance.Metrics

	// Let's use a default batch for now and expose it as configuration if needed.
	c.oidBatchSize = defaultOidBatchSize

	// metrics Configs
	if instance.UseGlobalMetrics {
		c.metrics = append(c.metrics, initConfig.GlobalMetrics...)
	}
	normalizeMetrics(c.metrics)

	c.metricTags = instance.MetricTags

	c.oidConfig.scalarOids = parseScalarOids(c.metrics, c.metricTags)
	c.oidConfig.columnOids = parseColumnOids(c.metrics)

	// Profile Configs
	var profiles profileDefinitionMap
	if len(initConfig.Profiles) > 0 {
		// TODO: [PERFORMANCE] Load init config custom profiles once for all integrations
		//   There are possibly multiple init configs
		customProfiles, err := loadProfiles(initConfig.Profiles)
		if err != nil {
			return snmpConfig{}, fmt.Errorf("failed to load custom profiles: %s", err)
		}
		profiles = customProfiles
	} else {
		defaultProfiles, err := loadDefaultProfiles()
		if err != nil {
			return snmpConfig{}, fmt.Errorf("failed to load default profiles: %s", err)
		}
		profiles = defaultProfiles
	}

	for _, profileDef := range profiles {
		normalizeMetrics(profileDef.Metrics)
	}

	c.profiles = profiles
	profile := instance.Profile

	if profile != "" {
		err = c.refreshWithProfile(profile)
		if err != nil {
			return snmpConfig{}, fmt.Errorf("failed to refresh with profile `%s`: %s", profile, err)
		}
	}
	validateMetrics(c.metrics)

	// TODO: [VALIDATION] Add missing error handling by looking at
	//   https://github.com/DataDog/integrations-core/blob/e64e2d18529c6c106f02435c5fdf2621667c16ad/snmp/datadog_checks/snmp/config.py

	// TODO: [VALIDATION] Validate metrics
	//  - metrics
	//  - metricTags
	//  Cases:
	//   - index transform:
	//     https://github.com/DataDog/integrations-core/blob/d31d3532e16cf8418a8b112f47359f14be5ecae1/snmp/datadog_checks/snmp/parsing/metrics.py#L523-L537
	return c, err
}

func getUptimeMetricConfig() metricsConfig {
	// Reference sysUpTimeInstance directly, see http://oidref.com/1.3.6.1.2.1.1.3.0
	return metricsConfig{Symbol: symbolConfig{OID: "1.3.6.1.2.1.1.3.0", Name: "sysUpTimeInstance"}}
}

func parseScalarOids(metrics []metricsConfig, metricTags []metricTagConfig) []string {
	var oids []string
	for _, metric := range metrics {
		if metric.Symbol.OID != "" { // TODO: [VALIDATION] need validation
			oids = append(oids, metric.Symbol.OID)
		}
	}
	for _, metricTag := range metricTags {
		if metricTag.OID != "" { // TODO: [VALIDATION] need validation
			oids = append(oids, metricTag.OID)
		}
	}
	return oids
}

func parseColumnOids(metrics []metricsConfig) []string {
	var oids []string
	for _, metric := range metrics {
		if metric.Table.OID != "" { // TODO: [VALIDATION] need validation
			for _, symbol := range metric.Symbols {
				oids = append(oids, symbol.OID)
			}
			for _, metricTag := range metric.MetricTags {
				if metricTag.Column.OID != "" {
					oids = append(oids, metricTag.Column.OID)
				}
			}
		}
	}
	return oids
}

func getProfileForSysObjectID(profiles profileDefinitionMap, sysObjectID string) (string, error) {
	tmpSysOidToProfile := map[string]string{}
	var matchedOids []string

	for profile, definition := range profiles {
		for _, oidPattern := range definition.SysObjectIds {
			found, err := filepath.Match(oidPattern, sysObjectID)
			if err != nil {
				log.Debugf("pattern error: %s", err)
				continue
			}
			if !found {
				continue
			}
			if matchedProfile, ok := tmpSysOidToProfile[oidPattern]; ok {
				return "", fmt.Errorf("profile %s has the same sysObjectID (%s) as %s", profile, oidPattern, matchedProfile)
			}
			tmpSysOidToProfile[oidPattern] = profile
			matchedOids = append(matchedOids, oidPattern)
		}
	}
	oid, err := getMostSpecificOid(matchedOids)
	if err != nil {
		return "", fmt.Errorf("failed to get most specific profile for sysObjectID `%s`, for matched oids %v: %s", sysObjectID, matchedOids, err)
	}
	return tmpSysOidToProfile[oid], nil
}