package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/pkg/errors"
)

func main() {
	if err := _main(); err != nil {
		log.Printf("%s", err)
		os.Exit(1)
	}
}

type Endpoint struct {
	file       string
	methodName string
	Group      string     `json:"group,omitempty"`
	Name       string     `json:"name"` // e.g. "chat.PostMessage"
	JSON       string     `json:"json"`
	Args       []Argument `json:"args,omitempty"`
	ReturnType string     `json:"return,omitempty"`
	SkipToken  bool       `json:"skip_token,omitempty"`
}

type Argument struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Required  bool   `json:"required,omitempty"`
	Default   string `json:"default,omitempty"`
	QueryName string `json:"query_name,omitempty"`
	Comment   string `json:"comment,omitempty"`
}

func camelit(s string) string {
	var buf bytes.Buffer

	var upnext bool
	for i, r := range s {
		if i == 0 || upnext {
			buf.WriteRune(unicode.ToUpper(r))
			upnext = false
			continue
		}

		if r == '.' || r == '_' {
			upnext = true
			continue
		}

		buf.WriteRune(r)
	}
	return buf.String()
}

func _main() error {
	var endpoints []Endpoint

	fh, err := os.Open("endpoints.json")
	if err != nil {
		return errors.Wrap(err, `failed to open endpoints.json`)
	}
	defer fh.Close()

	if err := json.NewDecoder(fh).Decode(&endpoints); err != nil {
		return errors.Wrap(err, `failed to decode endpoints.json`)
	}

	groups := map[string]struct{}{}
	group := map[string][]Endpoint{}
	for _, endpoint := range endpoints {
		i := strings.LastIndexByte(endpoint.Name, '.')
		endpoint.file = strings.Replace(endpoint.Name[:i], ".", "_", -1) + ".go"
		if len(endpoint.Group) == 0 {
			endpoint.Group = camelit(endpoint.Name[:i])
		}
		endpoint.methodName = camelit(endpoint.Name[i+1:])
		group[endpoint.file] = append(group[endpoint.file], endpoint)

		groups[endpoint.Group] = struct{}{}
	}

	if err := generateServicesFile(groups); err != nil {
		return errors.Wrap(err, `failed to generate services file`)
	}

	for fn, endpoints := range group {
		if err := generateServiceDetailsFile(fn, endpoints); err != nil {
			return errors.Wrapf(err, `failed to generate file %s`, fn)
		}
	}
	return nil
}

func generateServicesFile(groups map[string]struct{}) error {
	var list []string
	for k := range groups {
		list = append(list, k)
	}
	sort.Strings(list)

	var buf bytes.Buffer
	buf.WriteString("package slack")
	buf.WriteString("\n\n// Auto-generated by internal/cmd/genmethods/genmethods.go. DO NOT EDIT!")
	for _, g := range list {
		fmt.Fprintf(&buf, "\n\n// %sService handles %s related endpoints", g, g)
		fmt.Fprintf(&buf, "\ntype %sService struct {", g)
		buf.WriteString("\nclient *httpClient")
		buf.WriteString("\ntoken string")
		buf.WriteString("\n}")
	}

	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		log.Printf("%s", buf.Bytes())
		return errors.Wrap(err, `failed to format code`)
	}

	file := "services.go"
	fh, err := os.Create(file)
	if err != nil {
		return errors.Wrapf(err, `failed to open file %s for writing`, file)
	}
	defer fh.Close()

	fh.Write(formatted)
	return nil
}

func generateServiceDetailsFile(file string, endpoints []Endpoint) error {
	sort.Slice(endpoints, func(i, j int) bool {
		return strings.Compare(endpoints[i].Name, endpoints[j].Name) < 0
	})

	var buf bytes.Buffer
	buf.WriteString("\npackage slack")
	buf.WriteString("\n\n// Auto-generated by internal/cmd/genmethods/genmethods.go. DO NOT EDIT!")
	buf.WriteString("\n\nimport (")
	for _, pkg := range []string{"context", "net/url", "strconv"} {
		fmt.Fprintf(&buf, "\n%s", strconv.Quote(pkg))
	}
	buf.WriteString("\n")
	for _, pkg := range []string{"github.com/lestrrat/go-slack/objects", "github.com/pkg/errors"} {
		fmt.Fprintf(&buf, "\n%s", strconv.Quote(pkg))
	}
	buf.WriteString("\n)")

	buf.WriteString("\n\nvar _ = strconv.Itoa")
	buf.WriteString("\nvar _ = objects.EpochTime(0)")

	for _, endpoint := range endpoints {
		fmt.Fprintf(&buf, "\n// %s%sCall is created by %sService.%s method call", endpoint.Group, endpoint.methodName, endpoint.Group, endpoint.methodName)
		fmt.Fprintf(&buf, "\ntype %s%sCall struct {", endpoint.Group, endpoint.methodName)
		fmt.Fprintf(&buf, "\nservice *%sService", endpoint.Group)
		sort.Slice(endpoint.Args, func(i, j int) bool {
			return strings.Compare(endpoint.Args[i].Name, endpoint.Args[j].Name) < 0
		})

		for _, arg := range endpoint.Args {
			fmt.Fprintf(&buf, "\n%s %s", arg.Name, arg.Type)
			if len(arg.Comment) > 0 {
				buf.WriteString(" // ")
				buf.WriteString(arg.Comment)
			}
		}

		buf.WriteString("\n}")
	}

	for _, endpoint := range endpoints {
		var requiredArgNames []string
		var requiredArgList []string
		for _, arg := range endpoint.Args {
			if arg.Required {
				requiredArgNames = append(requiredArgNames, arg.Name)
				requiredArgList = append(requiredArgList, arg.Name+" "+arg.Type)
			}
		}

		fmt.Fprintf(&buf, "\n\n// %s creates a %s%sCall object in preparation for accessing the %s endpoint",
			endpoint.methodName, endpoint.Group, endpoint.methodName, endpoint.Name)
		fmt.Fprintf(&buf, "\nfunc (s *%sService) %s(%s) *%s%sCall{", endpoint.Group, endpoint.methodName, strings.Join(requiredArgList, ", "), endpoint.Group, endpoint.methodName)
		fmt.Fprintf(&buf, "\nvar call %s%sCall", endpoint.Group, endpoint.methodName)
		buf.WriteString("\ncall.service = s")
		for _, arg := range requiredArgNames {
			fmt.Fprintf(&buf, "\ncall.%s = %s", arg, arg)
		}
		buf.WriteString("\nreturn &call")
		buf.WriteString("\n}")

		for _, arg := range endpoint.Args {
			if arg.Required {
				continue
			}

			// If the type is *List, then we provide a SetXXX method. Similarly, a
			// singular XXX method is provided as a proxy to append to the list
			if strings.HasSuffix(arg.Type, "List") {
				fmt.Fprintf(&buf, "\n\n// Set%s sets the %s list", camelit(arg.Name), arg.Name)
				fmt.Fprintf(&buf, "\nfunc (c *%s%sCall) Set%s(%s %s) *%s%sCall {",
					endpoint.Group, endpoint.methodName, camelit(arg.Name), arg.Name, arg.Type, endpoint.Group, endpoint.methodName)
				fmt.Fprintf(&buf, "\nc.%s = %s", arg.Name, arg.Name)
				buf.WriteString("\nreturn c")
				buf.WriteString("\n}")

				var singularName string // hack
				if strings.HasSuffix(arg.Name, "es") {
					singularName = strings.TrimSuffix(arg.Name, "es")
				} else {
					singularName = strings.TrimSuffix(arg.Name, "s")
				}

				var singularType = strings.TrimSuffix(arg.Type, "List")
				fmt.Fprintf(&buf, "\n\n// %s appends to the %s list", camelit(singularName), arg.Name)
				fmt.Fprintf(&buf, "\nfunc (c *%s%sCall) %s(%s *%s) *%s%sCall {",
					endpoint.Group, endpoint.methodName, camelit(singularName), singularName, singularType, endpoint.Group, endpoint.methodName)
				fmt.Fprintf(&buf, "\nc.%s.Append(%s)", arg.Name, singularName)
				buf.WriteString("\nreturn c")
				buf.WriteString("\n}")
			} else {
				fmt.Fprintf(&buf, "\n\n// %s sets the value for optional %s parameter", camelit(arg.Name), arg.Name)
				fmt.Fprintf(&buf, "\nfunc (c *%s%sCall) %s(%s %s) *%s%sCall {",
					endpoint.Group, endpoint.methodName, camelit(arg.Name), arg.Name, arg.Type, endpoint.Group, endpoint.methodName)
				fmt.Fprintf(&buf, "\nc.%s = %s", arg.Name, arg.Name)
				buf.WriteString("\nreturn c")
				buf.WriteString("\n}")
			}
		}

		fmt.Fprintf(&buf, "\n\n// Values returns the %s%sCall object as url.Values", endpoint.Group, endpoint.methodName)
		fmt.Fprintf(&buf, "\nfunc (c *%s%sCall) Values() (url.Values, error) {", endpoint.Group, endpoint.methodName)
		buf.WriteString("\nv := url.Values{}")
		if !endpoint.SkipToken {
			buf.WriteString("\nv.Set(`token`, c.service.token)")
		}
		for _, arg := range endpoint.Args {
			var requiredCheck string
			var optionalCheck string
			var assignValue string
			var prelude string

			assignValue = fmt.Sprintf("c.%s", arg.Name)
			switch arg.Type {
			case "string":
				requiredCheck = fmt.Sprintf("\nif len(c.%s) <= 0 {", arg.Name)
				optionalCheck = fmt.Sprintf("\nif len(c.%s) > 0 {", arg.Name)
			case "bool":
				requiredCheck = fmt.Sprintf("\nif !c.%s {", arg.Name)
				optionalCheck = fmt.Sprintf("\nif c.%s {", arg.Name)
				assignValue = `"true"`
			case "int":
				requiredCheck = fmt.Sprintf("\nif c.%s == 0 {", arg.Name)
				optionalCheck = fmt.Sprintf("\nif c.%s > 0 {", arg.Name)
				assignValue = fmt.Sprintf(`strconv.Itoa(c.%s)`, arg.Name)
			default:
				prelude = fmt.Sprintf("\n%sEncoded, err := c.%s.Encode()\nif err != nil {\nreturn nil, errors.Wrap(err, `failed to encode field`)\n}", arg.Name, arg.Name)
				assignValue = fmt.Sprintf("%sEncoded", arg.Name)
				if strings.HasSuffix(arg.Type, "List") {
					requiredCheck = fmt.Sprintf("\nif len(c.%s) <= 0 {", arg.Name)
					optionalCheck = fmt.Sprintf("\nif len(c.%s) > 0 {", arg.Name)
				} else {
					requiredCheck = fmt.Sprintf("\nif c.%s == nil {", arg.Name)
					optionalCheck = fmt.Sprintf("\nif c.%s != nil {", arg.Name)
				}
			}

			buf.WriteString("\n")
			if arg.Required {
				buf.WriteString(requiredCheck)
				fmt.Fprintf(&buf, "\nreturn nil, errors.New(`missing required parameter %s`)", arg.Name)
				buf.WriteString("\n}")
				if len(prelude) > 0 {
					buf.WriteString(prelude)
				}
			} else {
				buf.WriteString(optionalCheck)
				if len(prelude) > 0 {
					buf.WriteString(prelude)
				}
			}

			var qn = arg.Name
			if len(arg.QueryName) > 0 {
				qn = arg.QueryName
			}
			fmt.Fprintf(&buf, "\nv.Set(%s,%s)", strconv.Quote(qn), assignValue)

			if !arg.Required {
				buf.WriteString("\n}")
			}
		}
		buf.WriteString("\nreturn v, nil")
		buf.WriteString("\n}")

		hasReturn := len(endpoint.ReturnType) > 0
		var returnType string
		if hasReturn {
			var rtbuf bytes.Buffer
			if !strings.HasSuffix(endpoint.ReturnType, "List") {
				rtbuf.WriteByte('*')
			}
			rtbuf.WriteString(endpoint.ReturnType)
			returnType = rtbuf.String()
		}

		fmt.Fprintf(&buf, "\n// Do executes the call to access %s endpoint", endpoint.Name)
		fmt.Fprintf(&buf, "\nfunc (c *%s%sCall) Do(ctx context.Context) ", endpoint.Group, endpoint.methodName)
		if hasReturn {
			fmt.Fprintf(&buf, "(%s, error)", returnType)
		} else {
			buf.WriteString("error")
		}
		buf.WriteString("{")
		fmt.Fprintf(&buf, "\nconst endpoint = %s", strconv.Quote(endpoint.Name))
		buf.WriteString("\nv, err := c.Values()")
		buf.WriteString("\nif err != nil {")
		buf.WriteString("\nreturn ")
		if hasReturn {
			buf.WriteString("nil, ")
		}
		buf.WriteString("err")
		buf.WriteString("\n}")
		buf.WriteString("\nvar res struct {")
		buf.WriteString("\nSlackResponse")
		if hasReturn {
			buf.WriteByte('\n')
			buf.WriteString(returnType)
			if endpoint.JSON != "" {
				buf.WriteString(fmt.Sprintf(" `json:\"%s\"`", endpoint.JSON))
			}
		}
		buf.WriteString("\n}")
		buf.WriteString("\nif err := c.service.client.postForm(ctx, endpoint, v, &res); err != nil {")
		buf.WriteString("\nreturn ")
		if hasReturn {
			buf.WriteString("nil, ")
		}
		fmt.Fprintf(&buf, "errors.Wrap(err, `failed to post to %s`)", endpoint.Name)
		buf.WriteString("\n}")

		buf.WriteString("\nif !res.OK {")
		buf.WriteString("\nreturn ")
		if hasReturn {
			buf.WriteString("nil, ")
		}
		buf.WriteString("errors.New(res.Error.String())")
		buf.WriteString("\n}")

		buf.WriteString("\n\nreturn ")
		if hasReturn {
			buf.WriteString("res.")
			if i := strings.LastIndexByte(endpoint.ReturnType, '.'); i > -1 {
				buf.WriteString(endpoint.ReturnType[i+1:])
			} else {
				buf.WriteString(endpoint.ReturnType)
			}
			buf.WriteString(", ")
		}
		buf.WriteString("nil")
		buf.WriteString("\n}")
	}
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		log.Printf("%s", buf.Bytes())
		return errors.Wrap(err, `failed to format code`)
	}

	fh, err := os.Create(file)
	if err != nil {
		return errors.Wrapf(err, `failed to open file %s for writing`, file)
	}
	defer fh.Close()

	fh.Write(formatted)
	return nil

}
