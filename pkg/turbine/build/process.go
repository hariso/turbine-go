package build

import (
	"context"
	"reflect"
	"strings"

	sdk "github.com/hariso/turbine-go/v2/pkg/turbine"
	pb "github.com/meroxa/turbine-core/lib/go/github.com/meroxa/turbine/core"
)

func (b *builder) Process(rs sdk.Records, fn sdk.Function) (sdk.Records, error) {
	return b.ProcessWithContext(context.Background(), rs, fn)
}

func (b *builder) ProcessWithContext(ctx context.Context, rs sdk.Records, fn sdk.Function) (sdk.Records, error) {
	c, err := b.AddProcessToCollection(
		ctx,
		&pb.ProcessCollectionRequest{
			Process: &pb.ProcessCollectionRequest_Process{
				Name: strings.ToLower(reflect.TypeOf(fn).Name()),
			},
			Collection: recordsToCollection(rs),
		})
	if err != nil {
		return sdk.Records{}, err
	}

	out := collectionToRecords(c)
	out.Records = fn.Process(out.Records)
	return out, nil
}
