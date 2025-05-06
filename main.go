package main

import (
	"fmt"      // 引入 fmt 包，用於格式化輸出
	"log"      // 引入 log 包，用於記錄日誌
	"net/http" // 引入 net/http 包，用於處理 HTTP 請求
	"sync"     // 引入 sync 包，用於提供基本的同步原語，如互斥鎖

	"github.com/gorilla/websocket" // 引入 gorilla/websocket 包，用於 WebSocket 通訊
)

// 前端送特別版http標頭 用此進行升級 即判斷是否有特殊標頭(即需求是ws) 若都有則升級 似乎會回101
var upgrader = websocket.Upgrader{ // 創建一個 WebSocket Upgrader 實例，用於處理 HTTP 連線升級到 WebSocket
	CheckOrigin: func(r *http.Request) bool { // 定義一個檢查 Origin 的函數
		return true // 允許所有來源的連線，正式環境請務必限制
	},
}

var clients = make(map[*websocket.Conn]string) // 儲存連線的 client (WebSocket 連線) 和其名稱 (string)
var broadcast = make(chan Message)             // 創建一個 Message 類型的 channel，用於廣播訊息
var onlineUsers = make(map[string]bool)        // 儲存當前在線的用戶名稱 (string) (使用 map 去重複)
var mu sync.Mutex                              // 創建一個互斥鎖，用於保護 onlineUsers 這個 map 的並發存取

type Message struct {
	Type    string   `json:"type"`    // 消息類型，可以是 "message", "join", "leave", "online_users"
	Name    string   `json:"name"`    // 發送者的名稱
	Content string   `json:"content"` // 消息的內容
	Users   []string `json:"users"`   // 用於傳送在線用戶列表
}

func handleConnections(w http.ResponseWriter, r *http.Request) { // 處理 WebSocket 連線的 HTTP Handler 函數
	// 先試著將 http 升級成 ws
	ws, err := upgrader.Upgrade(w, r, nil) // 將 HTTP 連線升級到 WebSocket 連線
	if err != nil {                        // 如果升級失敗
		log.Fatal(err) // 記錄錯誤並退出
	}
	defer ws.Close() // 確保函數退出時關閉 WebSocket 連線

	// 抓訊息
	var joinMsg Message                   // 聲明一個 Message 類型的變數用於接收加入消息
	err = ws.ReadJSON(&joinMsg)           // 從 WebSocket 連線中讀取 JSON 格式的消息到 joinMsg
	if err != nil || joinMsg.Name == "" { // 如果讀取錯誤或者使用者名稱為空
		log.Println("Error reading join message:", err) // 記錄讀取錯誤
		return                                          // 退出當前 goroutine
	}
	clientName := joinMsg.Name // 從加入消息中獲取使用者名稱

	// 兩個 map 加入當前連線
	mu.Lock()                      // 取得互斥鎖，保護 onlineUsers 的寫入
	onlineUsers[clientName] = true // 將新加入的使用者名稱添加到 onlineUsers map
	// 取得所有目前在線用戶
	currentOnlineUsers := getOnlineUsers() // 獲取當前在線用戶列表
	clients[ws] = clientName               // 將 WebSocket 連線和使用者名稱關聯存儲到 clients map
	mu.Unlock()                            // 釋放互斥鎖

	// 向新加入的用戶發送當前的在線用戶列表
	err = ws.WriteJSON(Message{Type: "online_users", Users: currentOnlineUsers}) // 將包含在線用戶列表的消息以 JSON 格式寫回 WebSocket 連線
	if err != nil {                                                              // 如果發送失敗
		// 紀錄失敗和從map中移除
		log.Println("Error sending online users list:", err) // 記錄發送錯誤
		delete(clients, ws)                                  // 從 clients map 中刪除該連線
		mu.Lock()                                            // 取得互斥鎖，保護 onlineUsers 的寫入
		delete(onlineUsers, clientName)                      // 從 onlineUsers map 中刪除該使用者名稱
		mu.Unlock()                                          // 釋放互斥鎖
		return                                               // 退出當前 goroutine
	}

	// 廣播用戶加入的消息
	broadcast <- Message{Type: "join", Name: "", Content: fmt.Sprintf("%s 加入了聊天室", clientName)} // 將用戶加入的消息發送到 broadcast channel
	log.Printf("%s 加入了聊天室\n", clientName)                                                       // 記錄用戶加入的日誌

	for { // 無限循環，用於持續讀取來自 WebSocket 連線的消息
		var msg Message          // 聲明一個 Message 類型的變數用於接收聊天消息
		err := ws.ReadJSON(&msg) // 從 WebSocket 連線中讀取 JSON 格式的消息到 msg
		if err != nil {          // 如果讀取錯誤，表示連線可能已斷開
			log.Printf("error reading json: %v", err)                                                    // 記錄讀取錯誤
			delete(clients, ws)                                                                          // 從 clients map 中刪除該連線
			mu.Lock()                                                                                    // 取得互斥鎖，保護 onlineUsers 的寫入
			delete(onlineUsers, clientName)                                                              // 從 onlineUsers map 中刪除該使用者名稱
			currentOnlineUsers := getOnlineUsers()                                                       // 獲取更新後的在線用戶列表
			mu.Unlock()                                                                                  // 釋放互斥鎖
			broadcast <- Message{Type: "leave", Name: "", Content: fmt.Sprintf("%s 離開了聊天室", clientName)} // 將用戶離開的消息發送到 broadcast channel
			broadcast <- Message{Type: "online_users", Users: currentOnlineUsers}                        // 廣播更新後的在線用戶列表
			log.Printf("%s 離開了聊天室\n", clientName)                                                        // 記錄用戶離開的日誌
			break                                                                                        // 退出循環，結束當前 goroutine
		}
		msg.Type = "message"                                   // 設置消息類型為 "message"
		msg.Name = clientName                                  // 補上發送者名稱
		broadcast <- msg                                       // 將收到的消息發送到 broadcast channel
		log.Printf("收到來自 %s 的訊息: %s\n", msg.Name, msg.Content) // 記錄收到的消息日誌
	}
}

func handleBroadcast() { // 處理廣播消息的函數，從 broadcast channel 中讀取消息並發送給所有在線 client
	for msg := range broadcast { // 無限循環，持續從 broadcast channel 中讀取消息
		for client, name := range clients { // 遍歷所有已連接的 client
			if msg.Type == "online_users" { // 如果消息類型是 "online_users"
				// 避免重複發送給發起離開的用戶
				if msg.Name != "" && name == msg.Name { // 如果消息包含發送者名稱且當前 client 是發送者
					continue // 跳過當前 client，不重複發送
				}
			}
			err := client.WriteJSON(msg) // 將消息以 JSON 格式寫回 WebSocket 連線
			if err != nil {              // 如果發送失敗
				log.Printf("error broadcasting message: %v", err) // 記錄發送錯誤
				client.Close()                                    // 關閉該 WebSocket 連線
				mu.Lock()                                         // 取得互斥鎖，保護 clients 和 onlineUsers 的寫入
				delete(clients, client)                           // 從 clients map 中刪除該連線
				delete(onlineUsers, name)                         // 從 onlineUsers map 中刪除該使用者名稱
				mu.Unlock()                                       // 釋放互斥鎖
			}
		}
	}
}

func getOnlineUsers() []string { // 獲取當前在線用戶列表的函數
	users := make([]string, 0, len(onlineUsers)) // 創建一個 string 類型的 slice，初始容量為 onlineUsers 的長度
	for user := range onlineUsers {              // 遍歷 onlineUsers map 的 key (使用者名稱)
		users = append(users, user) // 將使用者名稱添加到 users slice
	}
	return users // 返回包含所有在線用戶名稱的 slice
}

func main() {
	fs := http.FileServer(http.Dir("./dist")) // 創建一個文件伺服器，用於處理靜態文件，假設前端 build 後的檔案在 ./dist 目錄
	http.Handle("/", fs)                      // 將根路徑 "/" 的請求交給文件伺服器處理
	http.HandleFunc("/ws", handleConnections) // 將 "/ws" 路徑的請求交給 handleConnections 函數處理，用於建立 WebSocket 連線

	go handleBroadcast() // 在一個新的 goroutine 中運行 handleBroadcast 函數，處理廣播消息
	// 0.0.0.0:8080
	fmt.Println("Server started on :8080")              // 在控制台輸出伺服器啟動的消息
	log.Fatal(http.ListenAndServe("0.0.0.0:8080", nil)) // 啟動 HTTP 伺服器並監聽 8080 端口，如果發生錯誤則記錄並退出
}
