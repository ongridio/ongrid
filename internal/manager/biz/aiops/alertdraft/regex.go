package alertdraft

import "regexp"

var simpleHostMetricPredicateJoinRE = regexp.MustCompile(`(?i)\s+(and|or)\s+`)
var simpleHostMetricPredicatePartRE = regexp.MustCompile(`^\(?\s*([a-zA-Z_][a-zA-Z0-9_\-\s]*)\s*(==|!=|>=|<=|>|<)\s*([+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?)\s*\)?$`)
var promMetricNameRE = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
var nodeFilesystemMetricSelectorRE = regexp.MustCompile(`node_filesystem_(?:avail|free|size)_bytes\s*(\{[^}]*\})?`)
var promTrailingComparisonRE = regexp.MustCompile(`(?s)\s*(>=|<=|==|!=|>|<)\s*([+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?)\s*$`)
var promSimpleRangeSelectorRE = regexp.MustCompile(`\[(\d+(?:ms|s|m|h|d|w|y))\]`)
var promTokenRE = regexp.MustCompile(`[a-zA-Z_:][a-zA-Z0-9_:]*`)
var promMetricSourceIdentityMatcherRE = regexp.MustCompile(`(?i)\b(ongrid_source|device_id|job|instance|service)\s*(=|!=|=~|!~)\s*"([^"]+)"`)
var logLineFilterExprRE = regexp.MustCompile(`^\s*(\|=~|\|=|\|~|!=|!~)\s*(.+?)\s*$`)
var logLabelPrefixFilterRE = regexp.MustCompile(`(?i)^\s*(?:\(\?i\))?\s*(detected_level|device_id|filename|identifier|level|ongrid_source|priority|service_name|severity|unit)\s*(=|!=|=~|!~)\s*"?([A-Za-z0-9_:/-]+(?:\.[A-Za-z0-9_:/-]+)*)"?\s*(?:\.\*)?(.*)$`)
var logLabelAlternationPrefixFilterRE = regexp.MustCompile(`(?i)^\s*(?:\(\?i\))?\s*\(([^)]+)\)\s*\[=:\]\s*"?([A-Za-z0-9_:/-]+(?:\.[A-Za-z0-9_:/-]+)*)"?\s*(?:\.\*)?(.*)$`)
