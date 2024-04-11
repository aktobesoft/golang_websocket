package main

import (
	"bytes"
	"flag"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

func sendTo1C(link string, data string, user string, pass string) (responce string, statusCode int) {

	body := []byte(data)

	req, err := http.NewRequest("POST", link, bytes.NewBuffer(body))
	if err != nil {
		responce = err.Error()
		statusCode = 400
		return
	}

	req.Header.Add("Content-Type", "application/json")
	req.SetBasicAuth(user, pass)

	client := &http.Client{}
	res, err := client.Do(req)
	if err != nil {
		responce = err.Error()
		return
	}

	statusCode = res.StatusCode

	bodyText, _ := io.ReadAll(res.Body)
	defer res.Body.Close()
	responce = string(bodyText)

	return
}

func RestartSelf() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	args := os.Args
	env := os.Environ()
	// Windows does not support exec syscall.
	if runtime.GOOS == "windows" {
		cmd := exec.Command(self, args[1:]...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		cmd.Env = env
		err := cmd.Run()
		if err == nil {
			os.Exit(0)
		}
		return err
	}
	return syscall.Exec(self, args, env)
}

func generateErrorText(originalText string, text string) string {
	result := strings.Replace(originalText, "\"to\":", "\"tempto\":", 1)
	result = strings.Replace(result, "\"from\":", "\"to\":", 1)
	result = strings.Replace(result, "\"tempto\":", "\"from\":", 1)
	result = strings.Replace(result, "\"action\":", "\"result\":\""+text+"\", \"action\":", 1)
	return result
}

var addrPortal = flag.String("addrPortal", "smartdocs.kz", "Адрес портала интеграции")
var addr1C = flag.String("addr1C", "", "Адрес сервера 1C")
var user1C = flag.String("user1C", "", "Пользователь 1C")
var password1C = flag.String("password1C", "", "пароль пользователя 1С")
var bin_iin = flag.String("iin_bin", "", "ИИН/БИН Оргацнизации")
var secret_key = flag.String("secret_key", "", "Ключ для защиты от подмены")

func run_websocket() {
	flag.Parse()
	log.SetFlags(0)

	if *bin_iin == "" || *addr1C == "" || *user1C == "" {
		os.Exit(0)
	}

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	clientId := *bin_iin

	path := "/ws/" + clientId
	protocol := ""
	if *addrPortal == "smartdocs.kz" {
		protocol = "wss"
	} else {
		protocol = "ws"
	}

	u := url.URL{Scheme: protocol, Host: *addrPortal, Path: path}
	q := u.Query()
	q.Add("server_1c", "true")
	q.Add("transport", "websocket")

	if *secret_key != "" {
		q.Add("secret_key", *secret_key)
	}

	// Полный URL
	u.RawQuery = q.Encode()

	log.Printf("connecting to %s", u.String())

	for {

		// dialer := websocket.Dialer{}
		// c, _, err := dialer.DialContext(context.Background(), u.String(), nil)
		c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
		if err != nil {
			//пока не нажмут CTRL + C, будем пытаться подключиться к серверу
			select {
			case <-interrupt:
				return
			default:
				log.Printf("dial err:" + err.Error())
				log.Printf("wait 5 seconds to redail...")
				time.Sleep(time.Second * 5)
				continue
			}
		}

		log.Printf("connected...")
		defer c.Close()

		done := make(chan struct{})

		go func() {
			defer close(done)
			for {
				mt, message, err := c.ReadMessage()
				if err != nil {
					if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway) {
						log.Printf("unexpected close %v", err)
						RestartSelf()
					}
					return
				}
				log.Printf("recv: %s, type: %v", message, mt)

				tempAddres := *addr1C
				responceFrom1C := ""
				responceFrom1C, statusCode := sendTo1C(tempAddres, string(message), *user1C, *password1C)
				if !(statusCode >= 200 && statusCode <= 202) {
					responceFrom1C = generateErrorText(string(message), http.StatusText(statusCode))
				}
				err = c.WriteMessage(websocket.TextMessage, []byte(responceFrom1C))
				if err != nil {
					log.Println("write 1:", err)
					return
				}
				//log.Printf("send: %s, resp", responceFrom1C)
			}
		}()

		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()

		for {
			//пока не нажмут CTRL + C, будем принемать держать соединение
			select {
			case <-done:
				log.Println("error: done")
				RestartSelf()
				return
			case t := <-ticker.C:
				err := c.WriteMessage(websocket.TextMessage, []byte(t.String()))
				if err != nil {
					log.Println("write: time ticker", err)
					return
				}
			case <-interrupt:
				log.Println("interrupt")

				// Cleanly close the connection by sending a close message and then
				// waiting (with timeout) for the server to close the connection.
				err := c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				if err != nil {
					log.Println("write: interrupted", err)
					return
				}
				select {
				case <-done:
				case <-time.After(time.Second):
				}
				return
			}
		}
	}
}
