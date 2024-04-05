package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log/slog"
	"time"

	"github.com/bluesky-social/indigo/atproto/identity"
	"github.com/bluesky-social/indigo/atproto/syntax"

	es "github.com/opensearch-project/opensearch-go/v2"
	"go.opentelemetry.io/otel/attribute"
)

type EsSearchHit struct {
	Index  string          `json:"_index"`
	ID     string          `json:"_id"`
	Score  float64         `json:"_score"`
	Source json.RawMessage `json:"_source"`
}

type EsSearchHits struct {
	Total struct { // not used
		Value    int
		Relation string
	} `json:"total"`
	MaxScore float64       `json:"max_score"`
	Hits     []EsSearchHit `json:"hits"`
}

type EsSearchResponse struct {
	Took     int          `json:"took"`
	TimedOut bool         `json:"timed_out"`
	Hits     EsSearchHits `json:"hits"`
}

type UserResult struct {
	Did    string `json:"did"`
	Handle string `json:"handle"`
}

type PostSearchResult struct {
	Tid  string     `json:"tid"`
	Cid  string     `json:"cid"`
	User UserResult `json:"user"`
	Post any        `json:"post"`
}

type PostSearchQuery struct {
	Query  string     `json:"query"`
	Offset int        `json:"offset"`
	Size   int        `json:"size"`
	From   *time.Time `json:"from"`
	To     *time.Time `json:"to"`
	Actors []string   `json:"actors"`
	Tags   []string   `json:"tags"`
	Langs  []string   `json:"langs"`
}

type ActorSearchQuery struct {
	Query     string   `json:"query"`
	Following []string `json:"following"`
	Offset    int      `json:"offset"`
	Size      int      `json:"size"`
	Typeahead bool     `json:"typeahead"`
}

func checkParams(offset, size int) error {
	if offset+size > 10000 || size > 250 || offset > 10000 || offset < 0 || size < 0 {
		return fmt.Errorf("disallowed size/offset parameters")
	}
	return nil
}

func DoStructuredSearchPosts(ctx context.Context, dir identity.Directory, escli *es.Client, index string, q PostSearchQuery) (*EsSearchResponse, error) {
	ctx, span := tracer.Start(ctx, "DoStructuredSearchPosts")
	defer span.End()

	if err := checkParams(q.Offset, q.Size); err != nil {
		return nil, err
	}

	queryStr, filters := ParseQuery(ctx, dir, q.Query)
	basic := map[string]interface{}{
		"simple_query_string": map[string]interface{}{
			"query":            queryStr,
			"fields":           []string{"everything"},
			"flags":            "AND|NOT|OR|PHRASE|PRECEDENCE|WHITESPACE",
			"default_operator": "and",
			"lenient":          true,
			"analyze_wildcard": false,
		},
	}

	now := syntax.DatetimeNow()
	createdAtRange := map[string]interface{}{
		"lte": now,
	}

	if q.From != nil {
		createdAtRange["gte"] = syntax.Datetime(q.From.Format(syntax.AtprotoDatetimeLayout))
	}

	if q.To != nil {
		createdAtRange["lte"] = syntax.Datetime(q.To.Format(syntax.AtprotoDatetimeLayout))
	}

	timeRangeFilter := map[string]interface{}{
		"range": map[string]interface{}{
			"created_at": createdAtRange,
		},
	}

	filters = append(filters, timeRangeFilter)

	if len(q.Actors) > 0 {
		actorFilter := map[string]interface{}{
			"terms": map[string]interface{}{
				"did": q.Actors,
			},
		}
		filters = append(filters, actorFilter)
	}

	if len(q.Tags) > 0 {
		tagFilter := map[string]interface{}{
			"terms": map[string]interface{}{
				"tag": q.Tags,
			},
		}
		filters = append(filters, tagFilter)
	}

	if len(q.Langs) > 0 {
		langFilter := map[string]interface{}{
			"terms": map[string]interface{}{
				"lang": q.Langs,
			},
		}
		filters = append(filters, langFilter)
	}

	query := map[string]interface{}{
		"query": map[string]interface{}{
			"bool": map[string]interface{}{
				"must":   basic,
				"filter": filters,
			},
		},
		"sort": map[string]any{
			"created_at": map[string]any{
				"order": "desc",
			},
		},
		"size": q.Size,
		"from": q.Offset,
	}

	return doSearch(ctx, escli, index, query)
}

func DoSearchPosts(ctx context.Context, dir identity.Directory, escli *es.Client, index, q string, offset, size int) (*EsSearchResponse, error) {
	ctx, span := tracer.Start(ctx, "DoSearchPosts")
	defer span.End()

	if err := checkParams(offset, size); err != nil {
		return nil, err
	}
	queryStr, filters := ParseQuery(ctx, dir, q)
	basic := map[string]interface{}{
		"simple_query_string": map[string]interface{}{
			"query":            queryStr,
			"fields":           []string{"everything"},
			"flags":            "AND|NOT|OR|PHRASE|PRECEDENCE|WHITESPACE",
			"default_operator": "and",
			"lenient":          true,
			"analyze_wildcard": false,
		},
	}
	// filter out future posts (TODO: temporary hack)
	now := syntax.DatetimeNow()
	filters = append(filters, map[string]interface{}{
		"range": map[string]interface{}{
			"created_at": map[string]interface{}{
				"lte": now,
			},
		},
	})
	query := map[string]interface{}{
		"query": map[string]interface{}{
			"bool": map[string]interface{}{
				"must":   basic,
				"filter": filters,
			},
		},
		"sort": map[string]any{
			"created_at": map[string]any{
				"order": "desc",
			},
		},
		"size": size,
		"from": offset,
	}

	return doSearch(ctx, escli, index, query)
}

func DoSearchProfiles(ctx context.Context, dir identity.Directory, escli *es.Client, index, q string, offset, size int) (*EsSearchResponse, error) {
	ctx, span := tracer.Start(ctx, "DoSearchProfiles")
	defer span.End()

	if err := checkParams(offset, size); err != nil {
		return nil, err
	}

	queryStr, filters := ParseQuery(ctx, dir, q)
	basic := map[string]interface{}{
		"simple_query_string": map[string]interface{}{
			"query":            queryStr,
			"fields":           []string{"everything"},
			"flags":            "AND|NOT|OR|PHRASE|PRECEDENCE|WHITESPACE",
			"default_operator": "and",
			"lenient":          true,
			"analyze_wildcard": false,
		},
	}

	sort := map[string]interface{}{
		"pagerank": map[string]interface{}{
			"order": "desc",
		},
	}

	query := map[string]interface{}{
		"query": map[string]interface{}{
			"bool": map[string]interface{}{
				"must": basic,
				"should": []interface{}{
					map[string]interface{}{"term": map[string]interface{}{"has_avatar": true}},
					map[string]interface{}{"term": map[string]interface{}{"has_banner": true}},
				},
				"minimum_should_match": 0,
				"filter":               filters,
				"boost":                0.5,
			},
		},
		"size": size,
		"from": offset,
		"sort": sort,
	}

	return doSearch(ctx, escli, index, query)
}

func DoStructuredSearchProfiles(ctx context.Context, dir identity.Directory, escli *es.Client, index string, q ActorSearchQuery) (*EsSearchResponse, error) {
	ctx, span := tracer.Start(ctx, "DoStructuredSearchProfiles")
	defer span.End()

	span.SetAttributes(
		attribute.String("index", index),
		attribute.String("query", q.Query),
		attribute.Int("offset", q.Offset),
		attribute.Int("size", q.Size),
		attribute.Int("following", len(q.Following)),
	)

	if err := checkParams(q.Offset, q.Size); err != nil {
		return nil, err
	}

	queryStr, filters := ParseQuery(ctx, dir, q.Query)
	basic := map[string]interface{}{
		"simple_query_string": map[string]interface{}{
			"query":            queryStr,
			"fields":           []string{"everything"},
			"flags":            "AND|NOT|OR|PHRASE|PRECEDENCE|WHITESPACE",
			"default_operator": "and",
			"lenient":          true,
			"analyze_wildcard": false,
		},
	}

	sort := map[string]interface{}{
		"pagerank": map[string]interface{}{
			"order": "desc",
		},
	}

	if len(q.Following) > 0 {
		followingFilter := map[string]interface{}{
			"terms": map[string]interface{}{
				"did": q.Following,
			},
		}
		filters = append(filters, followingFilter)
	}

	query := map[string]interface{}{
		"query": map[string]interface{}{
			"bool": map[string]interface{}{
				"must": basic,
				"should": []interface{}{
					map[string]interface{}{"term": map[string]interface{}{"has_avatar": true}},
					map[string]interface{}{"term": map[string]interface{}{"has_banner": true}},
				},
				"minimum_should_match": 0,
				"filter":               filters,
				"boost":                0.5,
			},
		},
		"size": q.Size,
		"from": q.Offset,
		"sort": sort,
	}
	return doSearch(ctx, escli, index, query)
}

func DoSearchProfilesTypeahead(ctx context.Context, escli *es.Client, index, q string, size int) (*EsSearchResponse, error) {
	ctx, span := tracer.Start(ctx, "DoSearchProfilesTypeahead")
	defer span.End()

	if err := checkParams(0, size); err != nil {
		return nil, err
	}

	sort := map[string]interface{}{
		"pagerank": map[string]interface{}{
			"order": "desc",
		},
	}

	query := map[string]interface{}{
		"query": map[string]interface{}{
			"multi_match": map[string]interface{}{
				"query":    q,
				"type":     "bool_prefix",
				"operator": "and",
				"fields": []string{
					// adding handle here improves relevency but may be too expensive in prod
					//"handle^2",
					"typeahead",
					"typeahead._2gram",
					"typeahead._3gram",
				},
			},
		},
		"size": size,
		"sort": sort,
	}

	return doSearch(ctx, escli, index, query)
}

func DoStructuredSearchProfilesTypeahead(ctx context.Context, escli *es.Client, index string, q ActorSearchQuery) (*EsSearchResponse, error) {
	ctx, span := tracer.Start(ctx, "DoStructuredSearchProfilesTypeahead")
	defer span.End()

	span.SetAttributes(
		attribute.String("index", index),
		attribute.String("query", q.Query),
		attribute.Int("offset", q.Offset),
		attribute.Int("size", q.Size),
		attribute.Int("following", len(q.Following)),
	)

	if err := checkParams(q.Offset, q.Size); err != nil {
		return nil, err
	}

	var filters []map[string]interface{}

	sort := map[string]interface{}{
		"pagerank": map[string]interface{}{
			"order": "desc",
		},
	}

	if len(q.Following) > 0 {
		followingFilter := map[string]interface{}{
			"terms": map[string]interface{}{
				"did": q.Following,
			},
		}
		filters = append(filters, followingFilter)
	}

	query := map[string]interface{}{
		"query": map[string]interface{}{
			"bool": map[string]interface{}{
				"must": map[string]interface{}{
					"multi_match": map[string]interface{}{
						"query":    q.Query,
						"type":     "bool_prefix",
						"operator": "and",
						"fields": []string{
							"typeahead",
							"typeahead._2gram",
							"typeahead._3gram",
						},
					},
				},
				"filter": filters,
			},
		},
		"size": q.Size,
		"from": q.Offset,
		"sort": sort,
	}
	return doSearch(ctx, escli, index, query)
}

// helper to do a full-featured Lucene query parser (query_string) search, with all possible facets. Not safe to expose publicly.
func DoSearchGeneric(ctx context.Context, escli *es.Client, index, q string) (*EsSearchResponse, error) {
	ctx, span := tracer.Start(ctx, "DoSearchGeneric")
	defer span.End()

	query := map[string]interface{}{
		"query": map[string]interface{}{
			"query_string": map[string]interface{}{
				"query":                  q,
				"default_operator":       "and",
				"analyze_wildcard":       true,
				"allow_leading_wildcard": false,
				"lenient":                true,
				"default_field":          "everything",
			},
		},
	}

	return doSearch(ctx, escli, index, query)
}

func doSearch(ctx context.Context, escli *es.Client, index string, query interface{}) (*EsSearchResponse, error) {
	ctx, span := tracer.Start(ctx, "doSearch")
	defer span.End()

	span.SetAttributes(attribute.String("index", index), attribute.String("query", fmt.Sprintf("%+v", query)))

	b, err := json.Marshal(query)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize query: %w", err)
	}
	slog.Info("sending query", "index", index, "query", string(b))

	// Perform the search request.
	res, err := escli.Search(
		escli.Search.WithContext(ctx),
		escli.Search.WithIndex(index),
		escli.Search.WithBody(bytes.NewBuffer(b)),
	)
	if err != nil {
		return nil, fmt.Errorf("search query error: %w", err)
	}
	defer res.Body.Close()
	if res.IsError() {
		raw, err := ioutil.ReadAll(res.Body)
		if nil == err {
			slog.Warn("search query error", "resp", string(raw), "status_code", res.StatusCode)
		}
		return nil, fmt.Errorf("search query error, code=%d", res.StatusCode)
	}

	var out EsSearchResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding search response: %w", err)
	}

	return &out, nil
}
