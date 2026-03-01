// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package translator // import "github.com/open-telemetry/opentelemetry-collector-contrib/exporter/awsxrayexporter/internal/translator"

import (
	"net"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	conventions "go.opentelemetry.io/otel/semconv/v1.39.0"

	awsxray "github.com/open-telemetry/opentelemetry-collector-contrib/internal/aws/xray"
)

func makeHTTP(span ptrace.Span) (map[string]pcommon.Value, *awsxray.HTTPData) {
	var (
		info = awsxray.HTTPData{
			Request:  &awsxray.RequestData{},
			Response: &awsxray.ResponseData{},
		}
		filtered = make(map[string]pcommon.Value)
		urlParts = make(map[string]string)
	)

	if span.Attributes().Len() == 0 {
		return filtered, nil
	}

	hasHTTP := false
	hasHTTPRequestURLAttributes := false
	hasNetPeerAddr := false

	for key, value := range span.Attributes().All() {
		switch key {
		case string(conventions.HTTPRequestMethodKey):
			info.Request.Method = awsxray.String(value.Str())
			hasHTTP = true
		case string(conventions.UserAgentOriginalKey):
			info.Request.UserAgent = awsxray.String(value.Str())
			hasHTTP = true
		case string(conventions.HTTPResponseStatusCodeKey):
			info.Response.Status = aws.Int64(value.Int())
			hasHTTP = true
		case string(conventions.URLFullKey):
			urlParts[string(conventions.URLFullKey)] = value.Str()
			hasHTTP = true
			hasHTTPRequestURLAttributes = true
		case string(conventions.URLSchemeKey):
			urlParts[string(conventions.URLSchemeKey)] = value.Str()
			hasHTTP = true
		case string(conventions.URLPathKey):
			urlParts[key] = value.Str()
			hasHTTP = true
		case string(conventions.URLQueryKey):
			urlParts[key] = value.Str()
			hasHTTP = true
		case string(conventions.ServerAddressKey):
			urlParts[key] = value.Str()
			hasHTTPRequestURLAttributes = true
		case string(conventions.ServerPortKey):
			urlParts[key] = value.Str()
			if urlParts[key] == "" {
				urlParts[key] = strconv.FormatInt(value.Int(), 10)
			}
		case string(conventions.HostNameKey):
			urlParts[key] = value.Str()
			hasHTTPRequestURLAttributes = true
		case string(conventions.NetworkPeerAddressKey):
			// Prefer HTTP forwarded information (AttributeHTTPClientIP) when present.
			if net.ParseIP(value.Str()) != nil {
				if info.Request.ClientIP == nil {
					info.Request.ClientIP = awsxray.String(value.Str())
				}
				hasHTTP = true
				hasHTTPRequestURLAttributes = true
				hasNetPeerAddr = true
			}
		case string(conventions.ClientAddressKey):
			if net.ParseIP(value.Str()) != nil {
				info.Request.ClientIP = awsxray.String(value.Str())
				hasHTTP = true
			}
		default:
			filtered[key] = value
		}
	}

	if !hasNetPeerAddr && info.Request.ClientIP != nil {
		info.Request.XForwardedFor = aws.Bool(true)
	}

	if !hasHTTP {
		// Didn't have any HTTP-specific information so don't need to fill it in segment
		return filtered, nil
	}

	if hasHTTPRequestURLAttributes {
		if span.Kind() == ptrace.SpanKindServer {
			info.Request.URL = awsxray.String(constructServerURL(urlParts))
		} else {
			info.Request.URL = awsxray.String(constructClientURL(urlParts))
		}
	}

	info.Response.ContentLength = aws.Int64(extractResponseSizeFromEvents(span))

	return filtered, &info
}

func extractResponseSizeFromEvents(span ptrace.Span) int64 {
	// Support instrumentation that sets response size in span or as an event.
	size := extractResponseSizeFromAttributes(span.Attributes())
	if size != 0 {
		return size
	}
	for i := 0; i < span.Events().Len(); i++ {
		event := span.Events().At(i)
		size = extractResponseSizeFromAttributes(event.Attributes())
		if size != 0 {
			return size
		}
	}
	return size
}

func extractResponseSizeFromAttributes(attributes pcommon.Map) int64 {
	typeVal, ok := attributes.Get("message.type")
	if ok && typeVal.Str() == "RECEIVED" {
		if sizeVal, ok := attributes.Get(string(conventions.MessagingMessageBodySizeKey)); ok {
			return sizeVal.Int()
		}
	}
	return 0
}

func constructClientURL(urlParts map[string]string) string {
	// follows OpenTelemetry specification-defined combinations for client spans described in
	// https://github.com/open-telemetry/semantic-conventions/blob/main/docs/http/http-spans.md#http-client

	url, ok := urlParts[string(conventions.URLFullKey)]
	if ok {
		// full URL available so no need to assemble
		return url
	}

	scheme, ok := urlParts[string(conventions.URLSchemeKey)]
	if !ok {
		scheme = "http"
	}
	host, ok := urlParts[string(conventions.ServerAddressKey)]
	if !ok {
		host = ""
	}
	port, ok := urlParts[string(conventions.ServerPortKey)]
	if !ok {
		port = ""
	}
	url = scheme + "://" + host
	if port != "" && (scheme != "http" || port != "80") && (scheme != "https" || port != "443") {
		url += ":" + port
	}
	path, ok := urlParts[string(conventions.URLPathKey)]
	if ok {
		url += path
	} else {
		url += "/"
	}
	query, ok := urlParts[string(conventions.URLQueryKey)]
	if ok {
		if !strings.HasPrefix(query, "?") {
			query = "?" + query
		}
		url += query
	}
	return url
}

func constructServerURL(urlParts map[string]string) string {
	// follows OpenTelemetry specification-defined combinations for server spans described in
	// https://github.com/open-telemetry/semantic-conventions/blob/main/docs/http/http-spans.md#http-server

	url, ok := urlParts[string(conventions.URLFullKey)]
	if ok {
		// full URL available so no need to assemble
		return url
	}

	scheme, ok := urlParts[string(conventions.URLSchemeKey)]
	if !ok {
		scheme = "http"
	}
	host, ok := urlParts[string(conventions.ServerAddressKey)]
	if !ok {
		host, ok = urlParts[string(conventions.HostNameKey)]
		if !ok {
			host = ""
		}
	}
	port, ok := urlParts[string(conventions.ServerPortKey)]
	if !ok {
		port = ""
	}
	url = scheme + "://" + host
	if port != "" && (scheme != "http" || port != "80") && (scheme != "https" || port != "443") {
		url += ":" + port
	}
	path, ok := urlParts[string(conventions.URLPathKey)]
	if ok {
		url += path
	} else {
		url += "/"
	}
	query, ok := urlParts[string(conventions.URLQueryKey)]
	if ok {
		if !strings.HasPrefix(query, "?") {
			query = "?" + query
		}
		url += query
	}
	return url
}
