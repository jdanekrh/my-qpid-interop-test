package main

import (
	"log"
	"net"
	"net/url"
	"time"

	"encoding/json"
	"fmt"
	"math"
	"qpid.apache.org/amqp"
	"qpid.apache.org/electron"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func must(err error) {
	if err != nil {
		log.Fatalln(err)
	}
}

func connect(url *url.URL) (c electron.Connection, err error) {
	timeout := 15 * time.Second
	tcp, err := net.DialTimeout("tcp", url.Host, timeout)
	if err != nil {
		return
	}

	container := electron.NewContainer("")

	opts := make([]electron.ConnectionOption, 0)
	opts = append(opts, electron.SASLAllowInsecure(true))
	if url.User != nil {
		if u := url.User.Username(); u != "" {
			//glog.V(2).Infof("User: %v", u)
			opts = append(opts, electron.User(u))
		}
		if p, ok := url.User.Password(); ok {
			//glog.V(2).Infof("Password: %v", p)
			opts = append(opts, electron.Password([]byte(p)))
		}
	}

	c, err = container.Connection(tcp, opts...)
	return
}

func parse(type_ string, value interface{}) interface{} {
	switch v := value.(type) {
	case string:
		switch type_ {
		case "binary":
			return amqp.Binary(v)
		case "boolean":
			b, err := strconv.ParseBool(v)
			must(err)
			return b
		case "byte":
			b, err := strconv.ParseInt(v, 0, 8)
			must(err)
			return int8(b) // AMQP byte is not a Go byte
		case "decimal64", "decimal128":
			// TODO: no idea how to send this, let's send nothing
			return nil
		case "int":
			i, err := strconv.ParseInt(v, 0, 32)
			must(err)
			return int32(i)
		case "long":
			i, err := strconv.ParseInt(v, 0, 64)
			must(err)
			return int64(i)
		case "null", "none": // TODO: this is ugly
			return nil
		case "short":
			i, err := strconv.ParseInt(v, 0, 16)
			must(err)
			return int16(i)
		case "string":
			return string(v)
		case "symbol":
			return amqp.Symbol(v)
		case "timestamp":
			// TODO: no idea how to send this, let's send nothing
			return nil
		case "ubyte":
			i, err := strconv.ParseUint(v, 0, 8)
			must(err)
			return uint8(i)
		case "uint":
			i, err := strconv.ParseUint(v, 0, 32)
			must(err)
			return uint32(i)
		case "ulong":
			i, err := strconv.ParseUint(v, 0, 64)
			must(err)
			return uint64(i)
		case "ushort":
			i, err := strconv.ParseUint(v, 0, 16)
			must(err)
			return uint16(i)
		case "uuid":
			// TODO: no idea how to send this, let's send nothing
			return nil
		case "float":
			f, err := strconv.ParseFloat(v, 32)
			if err != nil {
				// hex bytes
				f, err := strconv.ParseUint(v, 0, 32)
				must(err)
				return math.Float32frombits(uint32(f))
			}
			return float32(f)
		case "double":
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				// hex bytes
				f, err := strconv.ParseUint(v, 0, 64)
				must(err)
				return math.Float64frombits(f)
			}
			return float64(f)
		case "char":
			// hex bytes
			f, err := strconv.ParseUint(v, 0, 32)
			if err != nil {
				// ascii
				if utf8.RuneCountInString(v) != 1 {
					log.Fatal("char contains more or less than one utf-8 char")
				}
				r, _ := utf8.DecodeRuneInString(v)
				return r
			}
			return rune(f)
		default:
			log.Fatalf("Wrong type %v given string value %v\n", type_, v)
		}
	case []interface{}:
		l := make([]interface{}, 0)
		for _, i := range v {
			l = append(l, parseValue(i))
		}
		return l
	case map[string]interface{}:
		m := make(map[interface{}]interface{})
		for k, v := range v {
			// think about the value of key, do I need typeswich?
			m[parseValue(k)] = parseValue(v)
		}
		return m
	}
	log.Fatalf("Wrong type %v given value %+v of type %T\n", type_, value, value)
	return nil
}

// parseValue is a helper for parse for parsing keys and values in nested lists and maps
func parseValue(v interface{}) interface{} {
	switch v := v.(type) {
	case string:
		return v
	case []interface{}:
		return parse("", v)
	case map[string]interface{}:
		return parse("", v)
	default:
		log.Fatalf("neither value nor nested map a map key, value %v type is %T", v, v)
		return nil
	}
}

// load is inverse function to parse
func load(type_ string, body interface{}) (string, interface{}) {
	// handle values which format differently top level and inside list or map
	switch type_ { // this is cheating, but it allows to handle top level char cases
	case "char":
		r := body.(rune)
		if r <= unicode.MaxASCII && (unicode.IsDigit(r) || unicode.IsLetter(r)) || unicode.IsSpace(r) {
			return "char", fmt.Sprintf("%c", body)
		}
		return "char", fmt.Sprintf("%#x", body)
	}
	switch body.(type) {
	case uint8, uint16, uint32, uint64, int8, int16, int32, int64:
		// format number as hex
		return GoToAMQPMapping[fmt.Sprintf("%T", body)], fmt.Sprintf("%#x", body)
	case nil:
		return "null", "None"
	}

	// everything else
	return loadValue(body)
}

var GoToAMQPMapping = map[string]string{
	"uint8":  "ubyte",
	"uint16": "ushort",
	"uint32": "uint",
	"uint64": "ulong",
	"int8":   "byte",
	"int16":  "short",
	"int32":  "int",
	"int64":  "long",
}

// loadValue is helper for load
func loadValue(v interface{}) (string, interface{}) {
	switch body := v.(type) {
	case nil:
		return "none", ""
	case string:
		return "string", body
	case uint8, uint16, uint32, uint64, int8, int16, int32, int64:
		return GoToAMQPMapping[fmt.Sprintf("%T", body)], fmt.Sprintf("%d", body) // d instead of x
	case float32:
		// TODO: what's more reasonable criterion?
		if (-10 <= body) && (body <= 10) {
			return "float", fmt.Sprint(body)
		}
		return "float", fmt.Sprintf("%#x", math.Float32bits(body))
	case float64:
		// TODO: what's more reasonable criterion?
		if (-10 <= body) && (body <= 10) {
			return "double", fmt.Sprint(body)
		}
		return "double", fmt.Sprintf("%#x", math.Float64bits(body))
	//case rune:
	// TODO: cannot distinguish int32 and rune types in Go, this is cheated around a bit in load() above
	case amqp.Binary:
		return "binary", v
	case bool:
		return "boolean", strings.Title(fmt.Sprint(body))
	case amqp.Symbol:
		return "symbol", fmt.Sprint(body)
	case amqp.List:
		l := make([]interface{}, 0)
		for _, v := range body {
			loadedt, loadedv := loadValue(v)
			switch loadedt {
			case "list", "map":
				l = append(l, loadedv)
			default:
				l = append(l, fmt.Sprintf("%s", loadedv))
			}
		}
		return "list", l
	case amqp.Map:
		m := make(map[string]interface{})
		for k, v := range body {
			var kk string
			keyt, keyv := loadValue(k)
			switch keyt {
			case "list", "map":
				log.Fatalln("load: cannot handle list or map in a map key yet")
			default:
				kk = fmt.Sprintf("%s", keyv.(string))
			}

			loadedt, loadedv := loadValue(v)
			switch loadedt {
			case "list", "map":
				m[kk] = loadedv
			default:
				m[kk] = fmt.Sprintf("%s", loadedv)
			}
		}
		return "map", m
	default:
		log.Panicf("loadValue: cannot decode %v of type %T", v, v)
		return "", nil
	}
}

func toString(bodies []interface{}) string {
	j, err := json.Marshal(bodies)
	must(err)
	return string(j)
}
