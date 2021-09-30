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

package precheck

import (
	"fmt"
	"testing"

	"github.com/erda-project/erda/modules/pipeline/precheck/prechecktype"
)

func TestPrecheck(t *testing.T) {
	ctx := prechecktype.InitContext()
	yamlByte := []byte(`
version: "1.1"
stages:
  - stage:
      - git-checkout:
          alias: git-checkout
          description: 代码仓库克隆
  - stage:
      - golang:
          alias: go-demo
          description: golang action
          params:
            command: GOPROXY=https://goproxy.io,direct go build -o web-server main.go
            context: ${git-checkout}
            service: web-server
            target: web-server
  - stage:
      - release:
          alias: release
          description: 用于打包完成时，向dicehub 提交完整可部署的dice.yml。用户若没在pipeline.yml里定义该action，CI会自动在pipeline.yml里插入该action
          params:
            dice_yml: ${git-checkout}/dice.yml
            image:
              go-demo: ${go-demo:OUTPUT:image}
  - stage:
      - dice:
          alias: dice
          description: 用于 dice 平台部署应用服务
          params:
            release_id: ${release:OUTPUT:releaseID}

`)
	items := prechecktype.ItemsForCheck{
		PipelineYml: "",
		Files: map[string]string{
			"dice.yml": `
version: '2.0'
services:
  go-demo:
    ports:
      - port: 5000
        expose: true
    resources:
      cpu: 0.5
      mem: 500
    deployments:
      replicas: 1
      selectors:
        location: go-demo
addons:
  fdf:
    plan: mysql:basic
    options:
      version: 5.7.29
envs: {}
`,
		},
		//ActionSpecs: map[string]apistructs.ActionSpec{
		//	"release": {
		//		Params: []apistructs.ActionSpecParam{
		//			{
		//				Name:     "cross_cluster",
		//				Required: false,
		//				Default:  true,
		//			},
		//		},
		//	},
		//},
	}
	ok, message := PreCheck(ctx, yamlByte, items)
	fmt.Println(message)
	fmt.Println(ok)
	//assert.False(t, prechecktype.GetContextResult(ctx, prechecktype.CtxResultKeyCrossCluster).(bool))
}
