// Package firestore provides a minimal read-only client for the Firestore REST
// API. It is enough to run structured queries against a database that allows
// unauthenticated reads, which is how the VAIMOO app publishes station and bike
// state.
package firestore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// Client queries a single Firestore database with an API key.
type Client struct {
	httpc   *http.Client
	project string
	apiKey  string
}

// New returns a client for the given project. httpc may be nil.
func New(httpc *http.Client, project, apiKey string) *Client {
	if httpc == nil {
		httpc = http.DefaultClient
	}
	return &Client{httpc: httpc, project: project, apiKey: apiKey}
}

// Filter is a single field comparison of a structured query.
type Filter struct {
	Field string
	Op    string
	Value any
}

// Equal returns a filter matching documents whose field equals value.
func Equal(field string, value any) Filter {
	return Filter{Field: field, Op: "EQUAL", Value: value}
}

// Query runs a structured query over one collection and decodes the matching
// documents into out, which must be a pointer to a slice.
//
// Field values are converted to plain JSON first, so struct tags and types work
// the way they do for any other JSON document. Integers arrive as JSON numbers
// even though Firestore encodes them as strings.
func (c *Client) Query(ctx context.Context, collection string, filters []Filter, out any) error {
	body, err := queryBody(collection, filters)
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf(
		"https://firestore.googleapis.com/v1/projects/%s/databases/(default)/documents:runQuery?key=%s",
		url.PathEscape(c.project), url.QueryEscape(c.apiKey),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("firestore: creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("firestore: performing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// The body is where a rejected filter or a missing index is explained.
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("firestore: http %s: %s", resp.Status, detail)
	}

	var results []struct {
		Document *struct {
			Name   string                     `json:"name"`
			Fields map[string]json.RawMessage `json:"fields"`
		} `json:"document"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return fmt.Errorf("firestore: decoding response: %w", err)
	}

	docs := make([]map[string]any, 0, len(results))
	for _, res := range results {
		// A query without matches answers with a single entry that has a read
		// time and no document.
		if res.Document == nil {
			continue
		}
		doc, err := decodeFields(res.Document.Fields)
		if err != nil {
			return fmt.Errorf("firestore: document %s: %w", res.Document.Name, err)
		}
		docs = append(docs, doc)
	}

	plain, err := json.Marshal(docs)
	if err != nil {
		return fmt.Errorf("firestore: re-encoding documents: %w", err)
	}
	if err := json.Unmarshal(plain, out); err != nil {
		return fmt.Errorf("firestore: decoding documents: %w", err)
	}
	return nil
}

func queryBody(collection string, filters []Filter) ([]byte, error) {
	query := map[string]any{
		"from": []any{map[string]any{"collectionId": collection}},
	}

	var encoded []any
	for _, f := range filters {
		value, err := encodeValue(f.Value)
		if err != nil {
			return nil, fmt.Errorf("firestore: filter on %s: %w", f.Field, err)
		}
		encoded = append(encoded, map[string]any{
			"fieldFilter": map[string]any{
				"field": map[string]any{"fieldPath": f.Field},
				"op":    f.Op,
				"value": value,
			},
		})
	}

	switch len(encoded) {
	case 0:
	case 1:
		query["where"] = encoded[0]
	default:
		query["where"] = map[string]any{
			"compositeFilter": map[string]any{"op": "AND", "filters": encoded},
		}
	}

	return json.Marshal(map[string]any{"structuredQuery": query})
}

func encodeValue(v any) (any, error) {
	switch v := v.(type) {
	case string:
		return map[string]any{"stringValue": v}, nil
	case bool:
		return map[string]any{"booleanValue": v}, nil
	case int:
		return map[string]any{"integerValue": strconv.Itoa(v)}, nil
	case int64:
		return map[string]any{"integerValue": strconv.FormatInt(v, 10)}, nil
	case float64:
		return map[string]any{"doubleValue": v}, nil
	default:
		return nil, fmt.Errorf("unsupported value type %T", v)
	}
}

func decodeFields(fields map[string]json.RawMessage) (map[string]any, error) {
	res := make(map[string]any, len(fields))
	for name, raw := range fields {
		value, err := decodeValue(raw)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", name, err)
		}
		res[name] = value
	}
	return res, nil
}

// decodeValue converts one Firestore typed value into its plain JSON form.
func decodeValue(raw json.RawMessage) (any, error) {
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, err
	}
	if len(wrapper) != 1 {
		return nil, fmt.Errorf("expected exactly one typed value, got %d", len(wrapper))
	}

	for kind, value := range wrapper {
		switch kind {
		case "nullValue":
			return nil, nil

		case "integerValue":
			// Firestore sends 64 bit integers as strings to survive JSON.
			var s string
			if err := json.Unmarshal(value, &s); err != nil {
				return nil, err
			}
			return json.Number(s), nil

		case "stringValue", "timestampValue", "bytesValue", "referenceValue",
			"booleanValue", "doubleValue", "geoPointValue":
			var v any
			if err := json.Unmarshal(value, &v); err != nil {
				return nil, err
			}
			return v, nil

		case "arrayValue":
			var arr struct {
				Values []json.RawMessage `json:"values"`
			}
			if err := json.Unmarshal(value, &arr); err != nil {
				return nil, err
			}
			res := make([]any, len(arr.Values))
			for i, item := range arr.Values {
				v, err := decodeValue(item)
				if err != nil {
					return nil, err
				}
				res[i] = v
			}
			return res, nil

		case "mapValue":
			var m struct {
				Fields map[string]json.RawMessage `json:"fields"`
			}
			if err := json.Unmarshal(value, &m); err != nil {
				return nil, err
			}
			return decodeFields(m.Fields)

		default:
			return nil, fmt.Errorf("unknown value kind %q", kind)
		}
	}

	return nil, nil
}
