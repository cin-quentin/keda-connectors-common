package common

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go/aws/credentials"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

// ConnectorMetadata contains common fields used by connectors
type ConnectorMetadata struct {
	Topic         string
	ResponseTopic string
	ErrorTopic    string
	HTTPEndpoint  string
	MaxRetries    int
	ContentType   string
	SourceName    string
}

type ErrorResponse struct {
	Status       int    `json:"status"`
	Message      string `json:"message"`
	HttpEndpoint string `json:"http_endpoint"`
	Source       string `json:"source"`
	Body         string `json:"body"`
	Request      string `json:"request"`
}

// ParseConnectorMetadata parses connector side common fields and returns as ConnectorMetadata or returns error
func ParseConnectorMetadata() (ConnectorMetadata, error) {
	for _, envVars := range []string{"TOPIC", "HTTP_ENDPOINT", "MAX_RETRIES", "CONTENT_TYPE"} {
		if os.Getenv(envVars) == "" {
			return ConnectorMetadata{}, fmt.Errorf("environment variable not found: %v", envVars)
		}
	}
	meta := ConnectorMetadata{
		Topic:         os.Getenv("TOPIC"),
		ResponseTopic: os.Getenv("RESPONSE_TOPIC"),
		ErrorTopic:    os.Getenv("ERROR_TOPIC"),
		HTTPEndpoint:  os.Getenv("HTTP_ENDPOINT"),
		ContentType:   os.Getenv("CONTENT_TYPE"),
		SourceName:    os.Getenv("SOURCE_NAME"),
	}
	if meta.SourceName == "" {
		meta.SourceName = "KEDAConnector"
	}
	val, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("MAX_RETRIES")), 0, 64)
	if err != nil {
		return ConnectorMetadata{}, fmt.Errorf("failed to parse value from MAX_RETRIES environment variable %v", err)
	}
	meta.MaxRetries = int(val)
	return meta, nil
}

// HandleHTTPRequest sends message and headers data to HTTP endpoint using POST method and returns response on success or error in case of failure
func HandleHTTPRequest(message string, headers http.Header, data ConnectorMetadata, logger *zap.Logger) (*http.Response, error) {

	var resp *http.Response
	for attempt := 0; attempt <= data.MaxRetries; attempt++ {
		// Create request
		req, err := http.NewRequest("POST", data.HTTPEndpoint, strings.NewReader(message))
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create HTTP request to invoke function. http_endpoint: %v, source: %v", data.HTTPEndpoint, data.SourceName)
		}

		// Add headers
		for key, vals := range headers {
			for _, val := range vals {
				req.Header.Add(key, val)
			}
		}

		// Make the request
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			logger.Error("sending function invocation request failed",
				zap.Error(err),
				zap.String("http_endpoint", data.HTTPEndpoint),
				zap.String("source", data.SourceName))
			continue
		}
		if resp == nil {
			continue
		}
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			// Success, quit retrying
			return resp, nil
		}
	}

	if resp == nil {
		errorResponce := ErrorResponse{
			Status:       503,
			Message:      "every function invocation retry failed; final retry gave empty response.",
			HttpEndpoint: data.HTTPEndpoint,
			Source:       data.SourceName,
			Request:      message,
		}
		jsonString, _ := json.Marshal(errorResponce)
		logger.Info(string(jsonString))
		return nil, fmt.Errorf(string(jsonString))
	}

	if resp.StatusCode < 200 || resp.StatusCode > 300 {
		defer resp.Body.Close()
		body, _ := ioutil.ReadAll(resp.Body)

		errorBody := ErrorResponse{
			Status:       resp.StatusCode,
			Message:      "request returned failure",
			HttpEndpoint: data.HTTPEndpoint,
			Source:       data.SourceName,
			Body:         string(body),
			Request:      message,
		}
		jsonString, _ := json.Marshal(errorBody)
		logger.Info(string(jsonString))
		return nil, fmt.Errorf(string(jsonString))
	}
	return resp, nil
}

// GetAwsConfig get's the configuration required to connect to aws
func GetAwsConfig() (*aws.Config, error) {
	if os.Getenv("AWS_REGION") == "" {
		return nil, errors.New("aws region required")
	}
	config := &aws.Config{
		Region: aws.String(os.Getenv("AWS_REGION")),
	}
	if os.Getenv("AWS_ENDPOINT") != "" {
		endpoint := os.Getenv("AWS_ENDPOINT")
		config.Endpoint = &endpoint
		return config, nil
	}
	if os.Getenv("AWS_ACCESS_KEY_ID") != "" && os.Getenv("AWS_SECRET_ACCESS_KEY") != "" {
		config.Credentials = credentials.NewStaticCredentials(os.Getenv("AWS_ACCESS_KEY_ID"),
			os.Getenv("AWS_SECRET_ACCESS_KEY"), "")
		return config, nil
	}
	if os.Getenv("AWS_CRED_PATH") != "" && os.Getenv("AWS_CRED_PROFILE") != "" {
		config.Credentials = credentials.NewSharedCredentials(os.Getenv("AWS_CRED_PATH"),
			os.Getenv("AWS_CRED_PROFILE"))
		return config, nil
	}
	return nil, errors.New("no aws configuration specified")
}
