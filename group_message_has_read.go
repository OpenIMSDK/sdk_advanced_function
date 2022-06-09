package sdk_advanced_function

import (
	"encoding/json"
	"errors"
	"github.com/golang/protobuf/proto"
	"github.com/jinzhu/copier"
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
	loginUserID string
	*db.DataBase
	platformID  int32
	ch          chan common.Cmd2Value
	msgListener open_im_sdk_callback.OnAdvancedMsgListener
}

func NewChatHasRead(ws *ws.Ws, loginUserID string, dataBase *db.DataBase, platformID int32, ch chan common.Cmd2Value, msgListener open_im_sdk_callback.OnAdvancedMsgListener) *ChatHasRead {
	return &ChatHasRead{Ws: ws, loginUserID: loginUserID, DataBase: dataBase, platformID: platformID, ch: ch, msgListener: msgListener}
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
			_ = common.TriggerCmdUpdateConversation(common.UpdateConNode{ConID: conversationID, Action: constant.UnreadCountSetZero}, c.ch)
			_ = common.TriggerCmdUpdateConversation(common.UpdateConNode{ConID: conversationID, Action: constant.ConChange, Args: []string{conversationID}}, c.ch)
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
		common.CheckAnyErrCallback(callback, 201, errors.New("message has been marked read or sender is yourself or notification message not support"), operationID)
	}

	for userID, list := range allUserMessage {
		s := sdk_struct.MsgStruct{}
		s.GroupID = groupID
		c.initBasicInfo(&s, constant.UserMsgType, constant.GroupHasReadReceipt, operationID)
		s.Content = utils.StructToJsonString(list)
		options := make(map[string]bool, 5)
		utils.SetSwitchFromOptions(options, constant.IsConversationUpdate, false)
		utils.SetSwitchFromOptions(options, constant.IsSenderConversationUpdate, false)
		utils.SetSwitchFromOptions(options, constant.IsUnreadCount, false)
		utils.SetSwitchFromOptions(options, constant.IsOfflinePush, false)
		//If there is an error, the coroutine ends, so judgment is not  required
		resp, _ := c.internalSendMessage(callback, &s, userID, "", operationID, &server_api_params.OfflinePushInfo{}, false, options)
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
			log.Error(operationID, "update message has read err", list, userID, err2.Error())
		}
	}
}

func (c *ChatHasRead) DoGroupMsgReadState(groupMsgReadList []*sdk_struct.MsgStruct) {
	var groupMessageReceiptResp []*sdk_struct.MessageReceipt
	var msgIDList []string
	for _, rd := range groupMsgReadList {
		err := json.Unmarshal([]byte(rd.Content), &msgIDList)
		if err != nil {
			log.Error("internal", "unmarshal failed, err : ", err.Error(), rd)
			continue
		}
		messages, err := c.GetMultipleMessage(msgIDList)
		if err != nil {
			log.Error("internal", "GetMessage err:", err.Error(), "ClientMsgID", msgIDList)
			continue
		}
		var msgIDListStatusOK []string
		if rd.SendID != c.loginUserID {
			for _, message := range messages {
				t := new(db.LocalChatLog)
				attachInfo := sdk_struct.AttachedInfoElem{}
				_ = utils.JsonStringToStruct(message.AttachedInfo, &attachInfo)
				attachInfo.GroupHasReadInfo.HasReadUserIDList = utils.RemoveRepeatedStringInList(append(attachInfo.GroupHasReadInfo.HasReadUserIDList, rd.SendID))
				attachInfo.GroupHasReadInfo.HasReadCount = int32(len(attachInfo.GroupHasReadInfo.HasReadUserIDList))
				t.AttachedInfo = utils.StructToJsonString(attachInfo)
				t.ClientMsgID = message.ClientMsgID
				t.IsRead = true
				err = c.UpdateMessage(t)
				if err != nil {
					log.Error("internal", "setMessageHasReadByMsgID err:", err, "ClientMsgID", t, message)
					continue
				}
				msgIDListStatusOK = append(msgIDListStatusOK, message.ClientMsgID)
			}
		} else {
			err = c.UpdateColumnsMessageList(msgIDList, map[string]interface{}{"is_read": true})
			if err != nil {
				log.Error("internal", "setMessageHasReadByMsgID err:", err, "ClientMsgID", msgIDList)
				continue
			}
			msgIDListStatusOK = append(msgIDListStatusOK, msgIDList...)
		}
		//var msgIDListStatusOK []string
		//for _, v := range msgIDList {
		//	t := new(model_struct.LocalChatLog)
		//	if rd.SendID != c.loginUserID {
		//		m, err := c.GetMessage(v)
		//		if err != nil {
		//			log.Error("internal", "GetMessage err:", err, "ClientMsgID", v)
		//			continue
		//		}
		//		attachInfo := sdk_struct.AttachedInfoElem{}
		//		_ = utils.JsonStringToStruct(m.AttachedInfo, &attachInfo)
		//		attachInfo.GroupHasReadInfo.HasReadUserIDList = utils.RemoveRepeatedStringInList(append(attachInfo.GroupHasReadInfo.HasReadUserIDList, rd.SendID))
		//		attachInfo.GroupHasReadInfo.HasReadCount = int32(len(attachInfo.GroupHasReadInfo.HasReadUserIDList))
		//		t.AttachedInfo = utils.StructToJsonString(attachInfo)
		//	}
		//	t.ClientMsgID = v
		//	t.IsRead = true
		//	err = c.UpdateMessage(t)
		//	if err != nil {
		//		log.Error("internal", "setMessageHasReadByMsgID err:", err, "ClientMsgID", v)
		//		continue
		//	}
		//	msgIDListStatusOK = append(msgIDListStatusOK, v)
		//}
		if len(msgIDListStatusOK) > 0 {
			msgRt := new(sdk_struct.MessageReceipt)
			msgRt.ContentType = rd.ContentType
			msgRt.MsgFrom = rd.MsgFrom
			msgRt.ReadTime = rd.SendTime
			msgRt.UserID = rd.SendID
			msgRt.GroupID = rd.GroupID
			msgRt.SessionType = constant.GroupChatType
			msgRt.MsgIDList = msgIDListStatusOK
			groupMessageReceiptResp = append(groupMessageReceiptResp, msgRt)
		}
	}
	if len(groupMessageReceiptResp) > 0 {
		log.Info("internal", "OnRecvGroupReadReceipt: ", utils.StructToJsonString(groupMessageReceiptResp))
		c.msgListener.OnRecvGroupReadReceipt(utils.StructToJsonString(groupMessageReceiptResp))
	}
}

func (c *ChatHasRead) NewDoGroupMsgReadState(groupMsgReadList []*sdk_struct.MsgStruct) {
	var groupMessageReceiptResp []*sdk_struct.MessageReceipt
	userMsgMap := make(map[string][]string)
	var otherMsgIDList []string
	var selfMsgIDList []string
	for _, rd := range groupMsgReadList {
		var list []string
		err := json.Unmarshal([]byte(rd.Content), &list)
		if err != nil {
			log.Error("internal", "unmarshal failed, err : ", err.Error(), rd)
			continue
		}
		if rd.SendID != c.loginUserID {
			if oldMsgIDList, ok := userMsgMap[rd.SendID]; ok {
				oldMsgIDList = append(oldMsgIDList, list...)
				userMsgMap[rd.SendID] = oldMsgIDList
			} else {
				userMsgMap[rd.SendID] = list
			}
			otherMsgIDList = append(otherMsgIDList, list...)
		} else {
			selfMsgIDList = append(selfMsgIDList, list...)
		}

	}
	newMsgID := utils.RemoveRepeatedStringInList(otherMsgIDList)
	messages, err := c.GetMultipleMessage(newMsgID)
	if err != nil {
		log.Error("internal", "GetMessage err:", err.Error(), "ClientMsgID", otherMsgIDList, newMsgID)
	}
	for _, message := range messages {
		t := new(db.LocalChatLog)
		attachInfo := sdk_struct.AttachedInfoElem{}
		_ = utils.JsonStringToStruct(message.AttachedInfo, &attachInfo)
		var temp []*sdk_struct.MessageReceipt
		for userID, list := range userMsgMap {
			if utils.IsContain(message.ClientMsgID, list) {
				msgRt := new(sdk_struct.MessageReceipt)
				msgRt.UserID = userID
				msgRt.GroupID = message.RecvID
				msgRt.SessionType = constant.GroupChatType
				msgRt.MsgIDList = []string{message.ClientMsgID}
				msgRt.ContentType = constant.GroupHasReadReceipt
				temp = append(temp, msgRt)
				attachInfo.GroupHasReadInfo.HasReadUserIDList = utils.RemoveRepeatedStringInList(append(attachInfo.GroupHasReadInfo.HasReadUserIDList, userID))
			}
		}
		attachInfo.GroupHasReadInfo.HasReadCount = int32(len(attachInfo.GroupHasReadInfo.HasReadUserIDList))
		t.AttachedInfo = utils.StructToJsonString(attachInfo)
		t.ClientMsgID = message.ClientMsgID
		t.IsRead = true
		err1 := c.UpdateMessage(t)
		if err1 != nil {
			log.Error("internal", "setGroupMessageHasReadByMsgID err:", err1, "ClientMsgID", t, message)
			continue
		}
		groupMessageReceiptResp = append(groupMessageReceiptResp, temp...)
	}
	newMsgID = utils.RemoveRepeatedStringInList(selfMsgIDList)
	selfMessages, err2 := c.GetMultipleMessage(newMsgID)
	if err2 != nil {
		log.Error("internal", "GetSelfSendMessage err:", err2.Error(), "ClientMsgID", selfMsgIDList, newMsgID)
	}
	for _, selfMessage := range selfMessages {
		t := new(db.LocalChatLog)
		t.ClientMsgID = selfMessage.ClientMsgID
		t.IsRead = true
		msgRt := new(sdk_struct.MessageReceipt)
		msgRt.UserID = c.loginUserID
		msgRt.GroupID = selfMessage.RecvID
		msgRt.SessionType = constant.GroupChatType
		msgRt.MsgIDList = []string{selfMessage.ClientMsgID}
		msgRt.ContentType = constant.GroupHasReadReceipt
		err = c.UpdateMessage(t)
		if err != nil {
			log.Error("internal", "setGroupMessageHasReadByMsgID err:", err, "ClientMsgID", t, selfMessage)
			continue
		}
		groupMessageReceiptResp = append(groupMessageReceiptResp, msgRt)
	}

	if len(groupMessageReceiptResp) > 0 {
		log.Info("internal", "OnRecvGroupReadReceipt: ", utils.StructToJsonString(groupMessageReceiptResp))
		c.msgListener.OnRecvGroupReadReceipt(utils.StructToJsonString(groupMessageReceiptResp))
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
func (c *ChatHasRead) internalSendMessage(callback open_im_sdk_callback.Base, s *sdk_struct.MsgStruct, recvID, groupID, operationID string, p *server_api_params.OfflinePushInfo, onlineUserOnly bool, options map[string]bool) (*server_api_params.UserSendMsgResp, error) {
	if recvID == "" && groupID == "" {
		common.CheckAnyErrCallback(callback, 201, errors.New("recvID && groupID not be allowed"), operationID)
	}
	if recvID == "" {
		s.SessionType = constant.GroupChatType
		s.GroupID = groupID
		groupMemberUidList, err := c.GetGroupMemberUIDListByGroupID(groupID)
		common.CheckAnyErrCallback(callback, 202, err, operationID)
		if !utils.IsContain(s.SendID, groupMemberUidList) {
			common.CheckAnyErrCallback(callback, 208, errors.New("you not exist in this group"), operationID)
		}

	} else {
		s.SessionType = constant.SingleChatType
		s.RecvID = recvID
	}

	if onlineUserOnly {
		options[constant.IsHistory] = false
		options[constant.IsPersistent] = false
		options[constant.IsOfflinePush] = false
		options[constant.IsSenderSync] = false
	}

	var wsMsgData server_api_params.MsgData
	copier.Copy(&wsMsgData, s)
	wsMsgData.Content = []byte(s.Content)
	wsMsgData.CreateTime = s.CreateTime
	wsMsgData.Options = options
	wsMsgData.OfflinePushInfo = p
	timeout := 300
	retryTimes := 0
	g, err := c.SendReqWaitResp(&wsMsgData, constant.WSSendMsg, timeout, retryTimes, c.loginUserID, operationID)
	switch e := err.(type) {
	case *constant.ErrInfo:
		common.CheckAnyErrCallback(callback, e.ErrCode, e, operationID)
	default:
		common.CheckAnyErrCallback(callback, 301, err, operationID)
	}
	var sendMsgResp server_api_params.UserSendMsgResp
	_ = proto.Unmarshal(g.Data, &sendMsgResp)
	return &sendMsgResp, nil

}
func msgStructToLocalChatLog(dst *db.LocalChatLog, src *sdk_struct.MsgStruct) {
	copier.Copy(dst, src)
	if src.SessionType == constant.GroupChatType {
		dst.RecvID = src.GroupID
	}
}
