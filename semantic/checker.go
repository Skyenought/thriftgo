// Copyright 2021 CloudWeGo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package semantic

import (
	"fmt"
	"log"
	"math"

	"github.com/cloudwego/thriftgo/parser"
)

// Checker reports whether there are semantic errors in the AST and produces
// warning messages for non-fatal errors.
type Checker interface {
	CheckAll(t *parser.Thrift) (warns []string, err error)
}

// Options controls the behavior of the default checker.
type Options struct {
	FixWarnings bool
}

type checker struct {
	Options
}

// NewChecker creates a checker.
func NewChecker(opt Options) Checker {
	return &checker{opt}
}

// ResolveSymbols is the global function with the same name.
func (c *checker) ResolveSymbols(t *parser.Thrift) error {
	return ResolveSymbols(t)
}

// CheckAll implements the Checker interface.
func (c *checker) CheckAll(t *parser.Thrift) (warns []string, err error) {
	checks := []func(t *parser.Thrift) ([]string, error){
		c.CheckGlobals,
		c.CheckEnums,
		c.CheckStructLikes,
		c.CheckUnions,
		c.CheckFunctions,
	}
	for tt := range t.DepthFirstSearch() {
		for _, f := range checks {
			ws, err := f(tt)
			warns = append(warns, ws...)
			if err != nil {
				return warns, err
			}
		}
	}
	return warns, nil
}

func (c *checker) CheckGlobals(t *parser.Thrift) (warns []string, err error) {
	defer func() {
		if e := recover(); e != nil {
			err = fmt.Errorf("[IDL grammar error] duplicated names in global scope: %s from file %s", e, t.Filename)
		}
	}()
	globals := make(map[string]bool)
	check := func(s string) {
		if globals[s] {
			panic(s)
		}
		globals[s] = true
	}
	for _, v := range t.Typedefs {
		check(v.Alias)
	}
	for _, v := range t.Constants {
		check(v.Name)
	}
	for _, v := range t.GetStructLikes() {
		check(v.Name)
	}
	for _, v := range t.Services {
		check(v.Name)
	}
	return
}

func (c *checker) CheckEnums(t *parser.Thrift) (warns []string, err error) {
	for _, e := range t.Enums {
		exist := make(map[string]bool)
		v2n := make(map[int64]string)
		for _, v := range e.Values {
			if exist[v.Name] {
				err = fmt.Errorf("[IDL grammar error] enum %s has duplicated value: %s from file %s", e.Name, v.Name, t.Filename)
			}
			exist[v.Name] = true
			if n, ok := v2n[v.Value]; ok && n != v.Name {
				err = fmt.Errorf(
					"[IDL grammar error] enum %s: duplicate value %d between '%s' and '%s' from file %s",
					e.Name, v.Value, n, v.Name, t.Filename,
				)
			}
			v2n[v.Value] = v.Name
			if err != nil {
				return
			}
			// check if enum value can be safely converted to int 32
			if v.Value < math.MinInt32 || v.Value > math.MaxInt32 {
				log.Printf("the value of enum %s is %d, which exceeds the range of int32. Please adjust its value to fit within the int32 range to avoid data errors during serialization!!!\n", v.Name, v.Value)
			}
		}
	}
	return
}

func (c *checker) CheckStructLikes(t *parser.Thrift) (warns []string, err error) {
	for _, s := range t.GetStructLikes() {
		fieldIDs := make(map[int32]bool)
		names := make(map[string]bool)
		for _, f := range s.Fields {
			if fieldIDs[f.ID] {
				err = fmt.Errorf("[IDL grammar error] duplicated field ID %d in %s %q from file %s",
					f.ID, s.Category, s.Name, t.Filename)
				return
			}
			if names[f.Name] {
				err = fmt.Errorf("[IDL grammar error] duplicated field name %q in %s %q from file %s",
					f.Name, s.Category, s.Name, t.Filename)
				return
			}
			fieldIDs[f.ID] = true
			names[f.Name] = true
			if f.ID <= 0 {
				warns = append(warns, fmt.Sprintf("non-positive ID %d of field %q in %q  from file %s",
					f.ID, f.Name, s.Name, t.Filename))
			}
		}
	}
	return
}

// CheckUnions checks the semantics of union nodes.
func (c *checker) CheckUnions(t *parser.Thrift) (warns []string, err error) {
	for _, u := range t.Unions {
		var hasDefault bool
		for _, f := range u.Fields {
			if f.Requiredness == parser.FieldType_Required {
				msg := fmt.Sprintf(
					"union %s field %s: union members must be optional, ignoring specified requiredness.",
					u.Name, f.Name)
				warns = append(warns, msg)
			}

			if f.GetDefault() != nil {
				if hasDefault {
					err = fmt.Errorf("[IDL grammar error] field %s provides another default value for union %s from file %s", f.Name, u.Name, t.Filename)
					return warns, err
				}
			}

			if c.FixWarnings {
				f.Requiredness = parser.FieldType_Optional
			}
		}
	}
	return
}

// CheckFunctions checks the semantics of service functions.
func (c *checker) CheckFunctions(t *parser.Thrift) (warns []string, err error) {
	var argOpt string
	for _, svc := range t.Services {
		defined := make(map[string]bool)
		for _, f := range svc.Functions {
			if defined[f.Name] {
				err = fmt.Errorf("[IDL grammar error] duplicated function name in %q: %q from file %s", svc.Name, f.Name, t.Filename)
				return
			}
			defined[f.Name] = true

			if f.Oneway && !f.Void {
				err = fmt.Errorf("[IDL grammar error] %s.%s: oneway function must be void type from file %s", svc.Name, f.Name, t.Filename)
				return
			}
			if f.Oneway && len(f.Throws) > 0 {
				err = fmt.Errorf("[IDL grammar error] %s.%s: oneway methods can't throw exceptions from file %s", svc.Name, f.Name, t.Filename)
				return
			}
			for _, a := range f.Arguments {
				if a.Requiredness == parser.FieldType_Optional {
					argOpt = t.Filename + ": optional keyword is ignored in argument lists."
					if c.FixWarnings {
						a.Requiredness = parser.FieldType_Default
					}
				}
				if a.ID <= 0 {
					warns = append(warns, fmt.Sprintf("non-positive ID %d of argument %q in %q.%q",
						a.ID, a.Name, svc.Name, f.Name))
				}
			}
			for _, a := range f.Throws {
				switch a.Requiredness {
				case parser.FieldType_Required:
					warns = append(warns, fmt.Sprintf("exception %q in %q.%q: throw field must be optional, ignoring specified requiredness.",
						a.Name, svc.Name, f.Name))
					if !c.FixWarnings {
						continue
					}
					fallthrough
				case parser.FieldType_Default:
					a.Requiredness = parser.FieldType_Optional
				}
			}
		}
	}
	if argOpt != "" {
		warns = append(warns, argOpt)
	}
	return
}
