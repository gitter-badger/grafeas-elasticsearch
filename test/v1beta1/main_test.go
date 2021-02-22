// Copyright 2021 The Rode Authors
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

package v1beta1_test

import (
	"flag"
	"github.com/brianvoe/gofakeit/v6"
	"log"
	"os"
	"testing"
)

var fake = gofakeit.New(0)

func TestMain(m *testing.M) {
	flag.Parse()

	if testing.Short() {
		log.Println("Test run with -short flag, skipping integration.")
		os.Exit(0)
	}

	os.Exit(m.Run())
}
