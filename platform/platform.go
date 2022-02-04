package platform

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/meroxa/meroxa-go/pkg/meroxa"
	"github.com/meroxa/valve"
	"log"
	"os"
	"reflect"
	"strings"
)

type Valve struct {
	client    *Client
	functions map[string]valve.Function
	deploy    bool
	imageName string
	config    valve.AppConfig
	secrets   map[string]string
}

func New(deploy bool, imageName string) Valve {
	c, err := newClient()
	if err != nil {
		log.Fatalln(err)
	}

	ac, err := valve.ReadAppConfig()
	if err != nil {
		log.Fatalln(err)
	}
	return Valve{
		client:    c,
		functions: make(map[string]valve.Function),
		imageName: imageName,
		deploy:    deploy,
		config:    ac,
		secrets:   make(map[string]string),
	}
}

func (v Valve) Resources(name string) (valve.Resource, error) {
	if !v.deploy {
		return Resource{}, nil
	}
	cr, err := v.client.GetResourceByNameOrID(context.Background(), name)
	if err != nil {
		return nil, err
	}

	log.Printf("retrieved resource %s (%s)", cr.Name, cr.Type)

	return Resource{
		ID:     cr.ID,
		Name:   cr.Name,
		Type:   string(cr.Type),
		client: v.client,
		v:      v,
	}, nil
}

type Resource struct {
	ID     int
	UUID   uuid.UUID
	Name   string
	Type   string
	client meroxa.Client
	v      Valve
}

func (r Resource) Records(collection string, cfg valve.ResourceConfigs) (valve.Records, error) {
	if r.client == nil {
		return valve.Records{}, nil
	}

	// TODO: ideally this should be handled on the platform
	mapCfg := cfg.ToMap()
	switch r.Type {
	case "redshift", "postgres", "mysql": // JDBC
		mapCfg["transforms"] = "createKey,extractInt"
		mapCfg["transforms.createKey.fields"] = "id"
		mapCfg["transforms.createKey.type"] = "org.apache.kafka.connect.transforms.ValueToKey"
		mapCfg["transforms.extractInt.field"] = "id"
		mapCfg["transforms.extractInt.type"] = "org.apache.kafka.connect.transforms.ExtractField$Key"
	}

	ci := &meroxa.CreateConnectorInput{
		ResourceID:    r.ID,
		Configuration: mapCfg,
		Type:          meroxa.ConnectorTypeSource,
		Input:         collection,
		PipelineName:  r.v.config.Pipeline,
	}

	con, err := r.client.CreateConnector(context.Background(), ci)
	if err != nil {
		return valve.Records{}, err
	}

	outStreams := con.Streams["output"].([]interface{})

	// Get first output stream
	out := outStreams[0].(string)

	log.Printf("created source connector to resource %s and write records to stream %s from collection %s", r.Name, out, collection)
	return valve.Records{
		Stream: out,
	}, nil
}

func (r Resource) Write(rr valve.Records, collection string, cfg valve.ResourceConfigs) error {
	// bail if dryrun
	if r.client == nil {
		return nil
	}

	// TODO: ideally this should be handled on the platform
	mapCfg := cfg.ToMap()
	switch r.Type {
	case "redshift", "postgres", "mysql": // JDBC sink
		mapCfg["table.name.format"] = strings.ToLower(collection)
		mapCfg["pk.mode"] = "record_value"
		mapCfg["pk.fields"] = "id"
		if r.Type != "redshift" {
			mapCfg["insert.mode"] = "upsert"
		}
	case "s3":
		mapCfg["aws_s3_prefix"] = strings.ToLower(collection) + "/"
		mapCfg["value.converter"] = "org.apache.kafka.connect.json.JsonConverter"
		mapCfg["value.converter.schemas.enable"] = "true"
		mapCfg["format.output.type"] = "jsonl"
		mapCfg["format.output.envelope"] = "true"
	}

	ci := &meroxa.CreateConnectorInput{
		ResourceID:    r.ID,
		Configuration: mapCfg,
		Type:          meroxa.ConnectorTypeDestination,
		Input:         rr.Stream,
		PipelineName:  r.v.config.Pipeline,
	}

	_, err := r.client.CreateConnector(context.Background(), ci)
	if err != nil {
		return err
	}
	log.Printf("created destination connector to resource %s and write records from stream %s to collection %s", r.Name, rr.Stream, collection)
	return nil
}

func (v Valve) Process(rr valve.Records, fn valve.Function) (valve.Records, valve.RecordsWithErrors) {
	// register function
	funcName := strings.ToLower(reflect.TypeOf(fn).Name())
	v.functions[funcName] = fn

	var out valve.Records
	var outE valve.RecordsWithErrors

	if v.deploy {
		// create the function
		cfi := CreateFunctionInput{
			InputStream: rr.Stream,
			Image:       v.imageName,
			EnvVars:     v.secrets,
			Args:        []string{funcName},
			Pipeline:    PipelineIdentifier{v.config.Pipeline},
		}

		log.Printf("creating function %s ...", funcName)
		fnOut, err := v.client.CreateFunction(context.Background(), &cfi)
		if err != nil {
			log.Panicf("unable to create function; err: %s", err.Error())
		}
		log.Printf("function %s created (%s)", funcName, fnOut.UUID)
		out.Stream = fnOut.OutputStream
	} else {
		// Not deploying, so map input stream to output stream
		out = rr
	}

	return out, outE
}

func (v Valve) TriggerFunction(name string, in []valve.Record) ([]valve.Record, []valve.RecordWithError) {
	log.Printf("Triggered function %s", name)
	return nil, nil
}

func (v Valve) GetFunction(name string) (valve.Function, bool) {
	fn, ok := v.functions[name]
	return fn, ok
}

func (v Valve) ListFunctions() []string {
	var funcNames []string
	for name := range v.functions {
		funcNames = append(funcNames, name)
	}

	return funcNames
}

// RegisterSecret pulls environment variables with the same name and ships them as Env Vars for functions
func (v Valve) RegisterSecret(name string) error {
	val := os.Getenv(name)
	if val == "" {
		return errors.New("secret is invalid or not set")
	}

	v.secrets[name] = val
	return nil
}
