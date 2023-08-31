# FastCrawling


     high performance replite framework

## 基本参数 
+ package: burp所抓取的数据包的文件所在路径(必填)
+ ip: 代理服务器的IP(选填,默认43.154.180.200)
+ port: 代理服务器的端口号(必填)
+ username: socks5代理的username(必填)
+ password: socks5代理的password(必填)
+ target: 爬取的数据存放位置(必填)
+ concurrent: 并发数量(选填,默认为1)
+ interval: 间隔时间(选填,默认为0)
+ from: 开始页数
+ to: 结束页数
+ step: 步长
+ list: 如果可变参数从txt文件里面读取
+ fixup: 当爬取到一半导致程序退出时，可选择使用fixup进行修补


在一个爬取数据中,(from,to,step) || （list）

## 注意
 
  1.当前支持两类可变参数,根据实际业务场景,在同一次爬取数据的时候,仅能用两种可变参数的其中一种
      
       + 定义 from to step
       + 定义list
  2. 支持使用 fixup 进行修复