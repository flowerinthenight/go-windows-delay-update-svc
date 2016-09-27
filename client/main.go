package main

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"regexp"
	"runtime"

	"github.com/urfave/cli"
)

const internalVersion = "1.0"

func traceln(v ...interface{}) {
	pc, _, _, _ := runtime.Caller(1)
	fn := runtime.FuncForPC(pc)
	fno := regexp.MustCompile(`^.*\.(.*)$`)
	fnName := fno.ReplaceAllString(fn.Name(), "$1")
	m := fmt.Sprintln(v...)
	log.Print("["+fnName+"] ", m)
}

func updateSelf(c *cli.Context) error {
	if c.String("file") == "" {
		err := fmt.Errorf("No file provided. See --file flag for more info.")
		traceln(err)
		return err
	}

	if c.String("host") == "" {
		err := fmt.Errorf("No host/ip provided. See --host flag for more info.")
		traceln(err)
		return err
	}

	reboot := true
	if c.IsSet("reboot") && c.Bool("reboot") == false {
		reboot = false
	}

	bodyBuf := &bytes.Buffer{}
	bodyWriter := multipart.NewWriter(bodyBuf)
	fileWriter, err := bodyWriter.CreateFormFile("uploadfile", c.String("file"))
	if err != nil {
		traceln(err)
		return err
	}

	fh, err := os.Open(c.String("file"))
	if err != nil {
		traceln(err)
		return err
	}

	_, err = io.Copy(fileWriter, fh)
	if err != nil {
		traceln(err)
		return err
	}

	contentType := bodyWriter.FormDataContentType()
	bodyWriter.Close()
	url := `http://` + c.String("host") + `:8080/api/v1/update/self`
	if !reboot {
		url = url + `?reboot=false`
	}

	resp, err := http.Post(url, contentType, bodyBuf)
	if err != nil {
		traceln(err)
		return err
	}

	defer resp.Body.Close()
	resp_body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		traceln(err)
		return err
	}

	traceln(resp.Status)
	traceln(string(resp_body))
	return nil
}

func main() {
	app := cli.NewApp()
	app.Name = "client"
	app.Usage = "client interface"
	app.Version = internalVersion
	app.Copyright = "(c) 2016 Chew Esmero"
	app.Commands = []cli.Command{
		{
			Name:  "update",
			Usage: "update module(s)",
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "file",
					Value: "",
					Usage: "new `file` to upload",
				},
				cli.StringFlag{
					Name:  "host",
					Value: "localhost",
					Usage: "target `host`",
				},
				cli.BoolFlag{
					Name:  "reboot",
					Usage: "should reboot after update (default: true)",
				},
			},
			ArgsUsage: "self",
			Action: func(c *cli.Context) error {
				if c.NArg() > 0 {
					switch c.Args().Get(0) {
					case "self":
						return updateSelf(c)
					default:
						traceln("Valid argument: 'self'.")
						return nil
					}
				} else {
					traceln("Not yet supported.")
				}

				return nil
			},
		},
	}

	app.Run(os.Args)
}
