package sdk_advanced_function

import (
	"encoding/json"
	"errors"
	"github.com/jinzhu/copier"
	conv "open_im_sdk/internal/conversation_msg"
	ws "open_im_sdk/internal/interaction"
	"open_im_sdk/open_im_sdk_callback"
	"open_im_sdk/pkg/common"
	"open_im_sdk/pkg/constant"
	"open_im_sdk/pkg/db"
	"open_im_sdk/pkg/log"
	"open_im_sdk/pkg/server_api_params"
	"open_im_sdk/pkg/utils"
	"open_im_sdk/sdk_struct"
)

type MarkGroupMessageAsReadParams []string

const MarkGroupMessageAsReadCallback = constant.SuccessCallbackDefault

type ChatHasRead struct {
	*ws.Ws
	conversation *conv.Conversation
	loginUserID  string
	*db.DataBase
	platformID int32
}

func NewChatHasRead(ws *ws.Ws, conversation *conv.Conversation, loginUserID string, dataBase *db.DataBase, platformID int32) *ChatHasRead {
	return &ChatHasRead{Ws: ws, conversation: conversation, loginUserID: loginUserID, DataBase: dataBase, platformID: platformID}
}

func (c *ChatHasRead) MarkGroupMessageAsRead(callback open_im_sdk_callback.Base, groupID string, msgIDList, operationID string) {
	if callback == nil {
		return
	}
	go func() {
		log.NewInfo(operationID, "MarkGroupMessageAsRead args: ", groupID, msgIDList)
		var unmarshalParams MarkGroupMessageAsReadParams
		common.JsonUnmarshalCallback(msgIDList, &unmarshalParams, callback, operationID)
		if len(unmarshalParams) == 0 {
			conversationID := utils.GetConversationIDBySessionType(groupID, constant.GroupChatType)
			_ = common.TriggerCmdUpdateConversation(common.UpdateConNode{ConID: conversationID, Action: constant.UnreadCountSetZero}, c.conversation.GetCh())
			_ = common.TriggerCmdUpdateConversation(common.UpdateConNode{ConID: conversationID, Action: constant.ConChange, Args: []string{conversationID}}, c.conversation.GetCh())
			callback.OnSuccess(MarkGroupMessageAsReadCallback)
			return
		}
		c.markGroupMessageAsRead(callback, unmarshalParams, groupID, operationID)
		callback.OnSuccess(MarkGroupMessageAsReadCallback)
		log.NewInfo(operationID, "MarkGroupMessageAsRead callback: ", MarkGroupMessageAsReadCallback)
	}()
}
func (c *ChatHasRead) markGroupMessageAsRead(callback open_im_sdk_callback.Base, msgIDList MarkGroupMessageAsReadParams, groupID, operationID string) {
	var localMessage db.LocalChatLog
	allUserMessage := make(map[string][]string, 3)
	messages, err := c.GetMultipleMessage(msgIDList)
	common.CheckDBErrCallback(callback, err, operationID)
	for _, v := range messages {
		if v.IsRead == false && v.ContentType < constant.NotificationBegin && v.SendID != c.loginUserID {
			if msgIDList, ok := allUserMessage[v.SendID]; ok {
				msgIDList = append(msgIDList, v.ClientMsgID)
				allUserMessage[v.SendID] = msgIDList
			} else {
				allUserMessage[v.SendID] = []string{v.ClientMsgID}
			}
		}
	}
	if len(allUserMessage) == 0 {
		common.CheckAnyErrCallback(callback, 201, errors.New("message has been marked read"), operationID)
	}

	for userID, list := range allUserMessage {
		s := sdk_struct.MsgStruct{}
		s.GroupID = groupID
		c.initBasicInfo(&s, constant.UserMsgType, constant.GroupHasReadReceipt, operationID)
		s.Content = utils.StructToJsonString(list)
		options := make(map[string]bool, 5)
		utils.SetSwitchFromOptions(options, constant.IsConversationUpdate, false)
		utils.SetSwitchFromOptions(options, constant.IsUnreadCount, false)
		utils.SetSwitchFromOptions(options, constant.IsOfflinePush, false)
		//If there is an error, the coroutine ends, so judgment is not  required
		resp, _ := c.conversation.InternalSendMessage(callback, &s, userID, "", operationID, &server_api_params.OfflinePushInfo{}, false, options)
		s.ServerMsgID = resp.ServerMsgID
		s.SendTime = resp.SendTime
		s.Status = constant.MsgStatusFiltered
		msgStructToLocalChatLog(&localMessage, &s)
		err = c.InsertMessage(&localMessage)
		if err != nil {
			log.Error(operationID, "inset into chat log err", localMessage, s, err.Error())
		}
		err2 := c.UpdateMessageHasRead(userID, list, constant.GroupChatType)
		if err2 != nil {
			log.Error(operationID, "update message has read error", list, userID, err2.Error())
		}
	}
}

func (c *ChatHasRead) DoGroupMsgReadState(groupMsgReadList []*sdk_struct.MsgStruct) {
	var groupMessageReceiptResp []*sdk_struct.GroupMessageReceipt
	var msgIdList []string
	for _, rd := range groupMsgReadList {
		err := json.Unmarshal([]byte(rd.Content), &msgIdList)
		if err != nil {
			log.Error("internal", "unmarshal failed, err : ", err.Error())
			return
		}
		var msgIdListStatusOK []string
		for _, v := range msgIdList {
			t := new(db.LocalChatLog)
			if rd.SendID != c.loginUserID {
				m, err := c.GetMessage(v)
				if err != nil {
					log.Error("internal", "GetMessage err:", err, "ClientMsgID", v)
					continue
				}
				attachInfo := sdk_struct.AttachedInfoElem{}
				_ = utils.JsonStringToStruct(m.AttachedInfo, &attachInfo)
				attachInfo.GroupHasReadInfo.HasReadCount++
				attachInfo.GroupHasReadInfo.HasReadUserIDList = append(attachInfo.GroupHasReadInfo.HasReadUserIDList, rd.SendID)
				t.AttachedInfo = utils.StructToJsonString(attachInfo)
			}
			t.ClientMsgID = v
			t.IsRead = true
			err = c.UpdateMessage(t)
			if err != nil {
				log.Error("internal", "setMessageHasReadByMsgID err:", err, "ClientMsgID", v)
				continue
			}
			msgIdListStatusOK = append(msgIdListStatusOK, v)
		}
		if len(msgIdListStatusOK) > 0 {
			msgRt := new(sdk_struct.GroupMessageReceipt)
			msgRt.ContentType = rd.ContentType
			msgRt.MsgFrom = rd.MsgFrom
			msgRt.ReadTime = rd.SendTime
			msgRt.UserID = rd.SendID
			msgRt.GroupID = rd.GroupID
			msgRt.SessionType = constant.GroupChatType
			msgRt.MsgIdList = msgIdListStatusOK
			groupMessageReceiptResp = append(groupMessageReceiptResp, msgRt)
		}
	}
	if len(groupMessageReceiptResp) > 0 {
		log.Info("internal", "OnRecvGroupReadReceipt: ", utils.StructToJsonString(groupMessageReceiptResp))
		c.conversation.MsgListener().OnRecvGroupReadReceipt(utils.StructToJsonString(groupMessageReceiptResp))
	}
}

func (c *ChatHasRead) initBasicInfo(message *sdk_struct.MsgStruct, msgFrom, contentType int32, operationID string) {
	message.CreateTime = utils.GetCurrentTimestampByMill()
	message.SendTime = message.CreateTime
	message.IsRead = false
	message.Status = constant.MsgStatusSending
	message.SendID = c.loginUserID
	userInfo, err := c.GetLoginUser()
	if err != nil {
		log.Error(operationID, "GetLoginUser", err.Error())
	} else {
		message.SenderFaceURL = userInfo.FaceURL
		message.SenderNickname = userInfo.Nickname
	}
	ClientMsgID := utils.GetMsgID(message.SendID)
	message.ClientMsgID = ClientMsgID
	message.MsgFrom = msgFrom
	message.ContentType = contentType
	message.SenderPlatformID = c.platformID
}
func msgStructToLocalChatLog(dst *db.LocalChatLog, src *sdk_struct.MsgStruct) {
	copier.Copy(dst, src)
	if src.SessionType == constant.GroupChatType {
		dst.RecvID = src.GroupID
	}
}
