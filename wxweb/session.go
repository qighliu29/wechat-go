/*
Copyright 2017 wechat-go Authors. All Rights Reserved.
MIT License

Copyright (c) 2017
Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
*/

package wxweb

import (
	"fmt"
	"github.com/mdp/qrterminal"
	"github.com/songtianyi/rrframework/config"
	"github.com/songtianyi/rrframework/logs"
	"github.com/songtianyi/rrframework/storage"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	// WEB_MODE: in this mode CreateSession will return a QRCode image url
	WEB_MODE = iota + 1
	// MINAL_MODE:  CreateSession will output qrcode in terminal
	TERMINAL_MODE
)

var (
	// DefaultCommon: default session config
	DefaultCommon = &Common{
		AppId:      "wx782c26e4c19acffb",
		LoginUrl:   "https://login.weixin.qq.com",
		Lang:       "zh_CN",
		DeviceID:   "e" + GetRandomStringFromNum(15),
		UserAgent:  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_11_3) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/58.0.3029.110 Safari/537.36",
		SyncSrv:    "webpush.wx2.qq.com",
		UploadUrl:  "https://file.wx2.qq.com/cgi-bin/mmwebwx-bin/webwxuploadmedia?f=json",
		MediaCount: 0,
	}
)

// Session: wechat bot session
type Session struct {
	WxWebCommon     *Common
	WxWebXcg        *XmlConfig
	Cookies         []*http.Cookie
	SynKeyList      *SyncKeyList
	Bot             *User
	Cm              *ContactManager
	QrcodePath      string //qrcode path
	QrcodeUUID      string //uuid
	HandlerRegister *HandlerRegister
}

// CreateSession: create wechat bot session
// if common is nil, session will be created with default config
// if handlerRegister is nil,  session will create a new HandlerRegister
func CreateSession(common *Common, handlerRegister *HandlerRegister, qrmode int) (*Session, error) {
	if common == nil {
		common = DefaultCommon
	}

	wxWebXcg := &XmlConfig{}

	// get qrcode
	uuid, err := JsLogin(common)
	if err != nil {
		return nil, err
	}
	logs.Info(uuid)
	session := &Session{
		WxWebCommon: common,
		WxWebXcg:    wxWebXcg,
		QrcodeUUID:  uuid,
	}

	if handlerRegister != nil {
		session.HandlerRegister = handlerRegister
	} else {
		session.HandlerRegister = CreateHandlerRegister()
	}

	if qrmode == TERMINAL_MODE {
		qrterminal.Generate("https://login.weixin.qq.com/l/"+uuid, qrterminal.L, os.Stdout)
	} else if qrmode == WEB_MODE {
		qrcb, err := QrCode(common, uuid)
		if err != nil {
			return nil, err
		}
		ls := rrstorage.CreateLocalDiskStorage("../public/qrcode/")
		if err := ls.Save(qrcb, "qrcode.jpg"); err != nil {
			return nil, err
		}
		session.QrcodePath = "/public/qrcode/" + uuid + ".jpg"
	}
	return session, nil
}

func (s *Session) analizeVersion(uri string) {
	u, _ := url.Parse(uri)

	// version may change
	s.WxWebCommon.CgiDomain = u.Scheme + "://" + u.Host
	s.WxWebCommon.CgiUrl = s.WxWebCommon.CgiDomain + "/cgi-bin/mmwebwx-bin"

	if strings.Contains(u.Host, "wx2") {
		// new version
		s.WxWebCommon.SyncSrv = "webpush.wx2.qq.com"
	} else {
		// old version
		s.WxWebCommon.SyncSrv = "webpush.wx.qq.com"
	}
}

func (s *Session) scanWaiter() error {
loop1:
	for {
		select {
		case <-time.After(3 * time.Second):
			redirectUri, err := Login(s.WxWebCommon, s.QrcodeUUID, "0")
			if err != nil {
				logs.Error(err)
				if strings.Contains(err.Error(), "window.code=408") {
					return err
				}
			} else {
				s.WxWebCommon.RedirectUri = redirectUri
				s.analizeVersion(s.WxWebCommon.RedirectUri)
				break loop1
			}
		}
	}
	return nil
}

// LoginAndServe: login wechat web and enter message receiving loop
func (s *Session) LoginAndServe(useCache bool) error {

	var (
		err error
	)

	if !useCache {
		if err := s.scanWaiter(); err != nil {
			return err
		}

		if s.Cookies, err = WebNewLoginPage(s.WxWebCommon, s.WxWebXcg, s.WxWebCommon.RedirectUri); err != nil {
			return err
		}
	}

	jb, err := WebWxInit(s.WxWebCommon, s.WxWebXcg)
	if err != nil {
		return err
	}

	jc, err := rrconfig.LoadJsonConfigFromBytes(jb)
	if err != nil {
		return err
	}

	s.SynKeyList, err = GetSyncKeyListFromJc(jc)
	if err != nil {
		return err
	}
	s.Bot, _ = GetUserInfoFromJc(jc)
	logs.Info(s.Bot)
	ret, err := WebWxStatusNotify(s.WxWebCommon, s.WxWebXcg, s.Bot)
	if err != nil {
		return err
	}
	if ret != 0 {
		return fmt.Errorf("WebWxStatusNotify fail, %d", ret)
	}

	cb, err := WebWxGetContact(s.WxWebCommon, s.WxWebXcg, s.Cookies)
	if err != nil {
		return err
	}

	s.Cm, err = CreateContactManagerFromBytes(cb)
	if err != nil {
		return err
	}

	s.Cm.AddContactFromUser(s.Bot)

	if err := s.serve(); err != nil {
		return err
	}
	return nil
}

func (s *Session) serve() error {
	msg := make(chan []byte, 1000)
	// syncheck
	errChan := make(chan error)
	go s.producer(msg, errChan)
	for {
		select {
		case m := <-msg:
			go s.consumer(m)
		case err := <-errChan:
			// all received messages have been consumed
			return err
		}
	}
}
func (s *Session) producer(msg chan []byte, errChan chan error) {
	logs.Info("entering synccheck loop")
loop1:
	for {
		ret, sel, err := SyncCheck(s.WxWebCommon, s.WxWebXcg, s.Cookies, s.WxWebCommon.SyncSrv, s.SynKeyList)
		// logs.Info(s.WxWebCommon.SyncSrv, ret, sel)
		if err != nil {
			logs.Error(err)
			continue
		}
		if ret == 0 {
			// check success
			if sel == 2 {
				// new message
				err := WebWxSync(s.WxWebCommon, s.WxWebXcg, s.Cookies, msg, s.SynKeyList)
				if err != nil {
					logs.Error(err)
				}
			} else if sel != 0 && sel != 7 {
				errChan <- fmt.Errorf("session down, sel %d", sel)
				break loop1
			}
		} else if ret == 1101 {
			errChan <- nil
			break loop1
		} else if ret == 1205 {
			errChan <- fmt.Errorf("api blocked, ret:%d", 1205)
			break loop1
		} else {
			errChan <- fmt.Errorf("unhandled exception ret %d", ret)
			break loop1
		}
	}

}

func (s *Session) consumer(msg []byte) {
	// analize message
	jc, _ := rrconfig.LoadJsonConfigFromBytes(msg)
	msgCount, _ := jc.GetInt("AddMsgCount")
	if msgCount < 1 {
		// no msg
		return
	}
	msgis, _ := jc.GetInterfaceSlice("AddMsgList")
	for _, v := range msgis {
		rmsg := s.analize(v.(map[string]interface{}))
		err, handles := s.HandlerRegister.Get(rmsg.MsgType)
		if err != nil {
			logs.Error(err)
			continue
		}
		for _, v := range handles {
			go v.Run(s, rmsg)
		}
	}
}

func (s *Session) analize(msg map[string]interface{}) *ReceivedMessage {
	rmsg := &ReceivedMessage{
		MsgId:         msg["MsgId"].(string),
		OriginContent: msg["Content"].(string),
		FromUserName:  msg["FromUserName"].(string),
		ToUserName:    msg["ToUserName"].(string),
		MsgType:       int(msg["MsgType"].(float64)),
	}

	if strings.Contains(rmsg.FromUserName, "@@") ||
		strings.Contains(rmsg.ToUserName, "@@") {
		rmsg.IsGroup = true
		// group message
		ss := strings.Split(rmsg.OriginContent, ":<br/>")
		if len(ss) > 1 {
			rmsg.Who = ss[0]
			rmsg.Content = ss[1]
		} else {
			rmsg.Who = s.Bot.UserName
			rmsg.Content = rmsg.OriginContent
		}
	}else{
		// no group message
		rmsg.Who = rmsg.FromUserName
		rmsg.Content = rmsg.OriginContent
	}
	if rmsg.MsgType == MSG_TEXT &&
		len(rmsg.Content) > 1 &&
		strings.HasPrefix(rmsg.Content, "@") {
		// @someone
		ss := strings.Split(rmsg.Content, "\u2005")
		if len(ss) == 2 {
			rmsg.At = ss[0] + "\u2005"
			rmsg.Content = ss[1]
		}
	}
	return rmsg
}

// SendText: send text msg type 1
func (s *Session) SendText(msg, from, to string) (string, string, error) {
	b, err := WebWxSendMsg(s.WxWebCommon, s.WxWebXcg, s.Cookies, from, to, msg)
	if err != nil {
		return "", "", err
	}
	jc, _ := rrconfig.LoadJsonConfigFromBytes(b)
	ret, _ := jc.GetInt("BaseResponse.Ret")
	if ret != 0 {
		errMsg, _ := jc.GetString("BaseResponse.ErrMsg")
		return "", "", fmt.Errorf("WebWxSendMsg Ret=%d, ErrMsg=%s", ret, errMsg)
	}
	msgID, _ := jc.GetString("MsgID")
	localID, _ := jc.GetString("LocalID")
	return msgID, localID, nil
}

// SendImg: send img, upload then send
func (s *Session) SendImg(path, from, to string) {
	ss := strings.Split(path, "/")
	b, err := ioutil.ReadFile(path)
	if err != nil {
		logs.Error(err)
		return
	}
	mediaId, err := WebWxUploadMedia(s.WxWebCommon, s.WxWebXcg, s.Cookies, ss[len(ss)-1], b)
	if err != nil {
		logs.Error(err)
		return
	}
	ret, err := WebWxSendMsgImg(s.WxWebCommon, s.WxWebXcg, s.Cookies, from, to, mediaId)
	if err != nil || ret != 0 {
		logs.Error(ret, err)
		return
	}
}

// SendImgFromBytes: send image from mem
func (s *Session) SendImgFromBytes(b []byte, path, from, to string) {
	ss := strings.Split(path, "/")
	mediaId, err := WebWxUploadMedia(s.WxWebCommon, s.WxWebXcg, s.Cookies, ss[len(ss)-1], b)
	if err != nil {
		logs.Error(err)
		return
	}
	ret, err := WebWxSendMsgImg(s.WxWebCommon, s.WxWebXcg, s.Cookies, from, to, mediaId)
	if err != nil || ret != 0 {
		logs.Error(ret, err)
		return
	}
}

// GetImg: get img by MsgId
func (s *Session) GetImg(msgId string) ([]byte, error) {
	return WebWxGetMsgImg(s.WxWebCommon, s.WxWebXcg, s.Cookies, msgId)
}

// SendEmotionFromPath: send gif, upload then send
func (s *Session) SendEmotionFromPath(path, from, to string) {
	ss := strings.Split(path, "/")
	b, err := ioutil.ReadFile(path)
	if err != nil {
		logs.Error(err)
		return
	}
	mediaId, err := WebWxUploadMedia(s.WxWebCommon, s.WxWebXcg, s.Cookies, ss[len(ss)-1], b)
	if err != nil {
		logs.Error(err)
		return
	}
	ret, err := WebWxSendEmoticon(s.WxWebCommon, s.WxWebXcg, s.Cookies, from, to, mediaId)
	if err != nil || ret != 0 {
		logs.Error(ret, err)
	}
}

// SendEmotionFromBytes: send gif/emoji from mem
func (s *Session) SendEmotionFromBytes(b []byte, from, to string) {
	mediaId, err := WebWxUploadMedia(s.WxWebCommon, s.WxWebXcg, s.Cookies, from+".gif", b)
	if err != nil {
		logs.Error(err)
		return
	}
	ret, err := WebWxSendEmoticon(s.WxWebCommon, s.WxWebXcg, s.Cookies, from, to, mediaId)
	if err != nil || ret != 0 {
		logs.Error(ret, err)
	}
}

// RevokeMsg: revoke message
func (s *Session) RevokeMsg(clientMsgId, svrMsgId, toUserName string) {
	err := WebWxRevokeMsg(s.WxWebCommon, s.WxWebXcg, s.Cookies, clientMsgId, svrMsgId, toUserName)
	if err != nil {
		logs.Error("revoke msg %s failed, %s", clientMsgId+":"+svrMsgId, err)
		return
	}
}

// Logout: logout web wechat

func (s *Session) Logout() error {
	return WebWxLogout(s.WxWebCommon, s.WxWebXcg, s.Cookies)
}
