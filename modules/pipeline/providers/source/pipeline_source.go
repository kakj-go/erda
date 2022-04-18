// Copyright (c) 2021 Terminus, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package source

import (
	"context"
	"time"

	"github.com/erda-project/erda-proto-go/core/pipeline/source/pb"
	"github.com/erda-project/erda/modules/pipeline/providers/source/db"
)

type pipelineSource struct {
	dbClient *db.Client
}

func (p pipelineSource) Create(ctx context.Context, request *pb.PipelineSourceCreateRequest) (*pb.PipelineSourceCreateResponse, error) {
	result, err := p.Save(ctx, &pb.PipelineSourceSaveRequest{
		SourceType: request.SourceType,
		Remote:     request.Remote,
		Ref:        request.Ref,
		Path:       request.Path,
		Name:       request.Name,
		PipelineYml: request.PipelineYml,
	})
	if err != nil {
		return nil, err
	}

	return &pb.PipelineSourceCreateResponse{PipelineSource: result.PipelineSource}, nil
}

func (p pipelineSource) Update(ctx context.Context, request *pb.PipelineSourceUpdateRequest) (*pb.PipelineSourceUpdateResponse, error) {
	source, err := p.dbClient.GetPipelineSource(request.PipelineSourceID)
	if err != nil {
		return nil, err
	}

	if request.PipelineYml != "" {
		source.PipelineYml = request.PipelineYml
	}
	err = p.dbClient.UpdatePipelineSource(request.PipelineSourceID, source)
	if err != nil {
		return nil, err
	}
	return &pb.PipelineSourceUpdateResponse{
		PipelineSource: source.Convert(),
	}, nil
}

func (p pipelineSource) Delete(ctx context.Context, request *pb.PipelineSourceDeleteRequest) (*pb.PipelineSourceDeleteResponse, error) {
	source, err := p.dbClient.GetPipelineSource(request.PipelineSourceID)
	if err != nil {
		return nil, err
	}
	source.SoftDeletedAt = uint64(time.Now().UnixNano() / 1e6)

	return &pb.PipelineSourceDeleteResponse{}, p.dbClient.DeletePipelineSource(request.PipelineSourceID, source)
}

func (p pipelineSource) Get(ctx context.Context, request *pb.PipelineSourceGetRequest) (*pb.PipelineSourceGetResponse, error) {
	source, err := p.dbClient.GetPipelineSource(request.GetPipelineSourceID())
	if err != nil {
		return nil, err
	}
	return &pb.PipelineSourceGetResponse{PipelineSource: source.Convert()}, nil
}

func (p pipelineSource) List(ctx context.Context, request *pb.PipelineSourceListRequest) (*pb.PipelineSourceListResponse, error) {
	unique := &db.PipelineSourceUnique{
		SourceType: request.SourceType,
		Remote:     request.Remote,
		Ref:        request.Ref,
		Path:       request.Path,
		Name:       request.Name,
		IDList:     request.IdList,
	}

	var sources []db.PipelineSource
	var err error
	if request.IdList != nil {
		sources, err = p.dbClient.ListPipelineSource(request.IdList)
	} else {
		sources, err = p.dbClient.GetPipelineSourceByUnique(unique)
	}
	if err != nil {
		return nil, err
	}

	data := make([]*pb.PipelineSource, 0, len(sources))
	for _, v := range sources {
		data = append(data, v.Convert())
	}

	return &pb.PipelineSourceListResponse{Data: data}, nil
}

func (p pipelineSource) Save(ctx context.Context, request *pb.PipelineSourceSaveRequest) (*pb.PipelineSourceSaveResponse, error) {
	unique := &db.PipelineSourceUnique{
		SourceType: request.SourceType,
		Remote:     request.Remote,
		Ref:        request.Ref,
		Path:       request.Path,
		Name:       request.Name,
	}

	sources, err := p.dbClient.GetPipelineSourceByUnique(unique)
	if err != nil {
		return nil, err
	}

	var source *db.PipelineSource
	if len(sources) > 1 {
		source = &sources[0]
		source.PipelineYml = request.PipelineYml
		err = p.dbClient.UpdatePipelineSource(source.ID, source)
		if err != nil {
			return nil, err
		}
	}else{
		source = &db.PipelineSource{
			SourceType:  request.SourceType,
			Remote:      request.Remote,
			Ref:         request.Ref,
			Path:        request.Path,
			Name:        request.Name,
			PipelineYml: request.PipelineYml,
		}
		if err = p.dbClient.CreatePipelineSource(source); err != nil {
			return nil, err
		}
	}
	return &pb.PipelineSourceSaveResponse{PipelineSource: source.Convert()}, nil
}
