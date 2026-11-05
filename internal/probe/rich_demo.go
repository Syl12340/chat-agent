package probe

// richTextDemo is deterministic and runs only under explicit -demo.
func richTextDemo() string {
	return "# 富文本与数学\n" +
		"这是 **本地演示**：支持 ~~旧想法~~、嵌套列表和只读任务。\n" +
		"\n" +
		"- [x] 已展示\n" +
		"  - 嵌套条目\n" +
		"- [ ] 待讨论的想法\n" +
		"\n" +
		"| 内容 | 例子 |\n" +
		"| --- | --- |\n" +
		"| 行内公式 | \\(a^2+b^2=c^2\\) |\n" +
		"| 普通价格 | $5 和 $10 |\n" +
		"\n" +
		"独立公式：\n" +
		"\\[\n" +
		"\\frac{1}{2} + \\sum_{k=1}^{3}k = \\frac{13}{2}\n" +
		"\\]\n" +
		"\n" +
		"矩阵：\n" +
		"$$\n" +
		"\\begin{pmatrix}1 & 2 \\\\ 3 & 4\\end{pmatrix}\n" +
		"$$\n" +
		"\n" +
		"代码保持原样：`\\(a+b\\)`。\n" +
		"\n" +
		"```json\n" +
		"{\"title\":\"本地数据\",\"items\":[{\"name\":\"A\",\"value\":2},{\"name\":\"B\",\"value\":3}],\"active\":true}\n" +
		"```\n" +
		"\n" +
		"```chat-ui\n" +
		"{\"schema_version\":1,\"id\":\"table-1\",\"type\":\"table.data\",\"props\":{\"title\":\"本地示例\",\"columns\":[{\"id\":\"name\",\"label\":\"项目\"},{\"id\":\"value\",\"label\":\"值\"}],\"rows\":[[\"A\",2],[\"B\",3]]}}\n" +
		"```\n" +
		"\n" +
		"```chat-ui\n" +
		"{\"schema_version\":1,\"id\":\"bars-1\",\"type\":\"chart.bar\",\"props\":{\"title\":\"对比\",\"categories\":[\"A\",\"B\",\"C\"],\"series\":[{\"name\":\"示例值\",\"values\":[2,-1,4]}]}}\n" +
		"```\n" +
		"\n" +
		"```chat-ui\n" +
		"{\"schema_version\":1,\"id\":\"flow-1\",\"type\":\"diagram.flow\",\"props\":{\"title\":\"一个小流程\",\"nodes\":[{\"id\":\"a\",\"label\":\"开始\"},{\"id\":\"b\",\"label\":\"试一小步\"},{\"id\":\"c\",\"label\":\"回顾\"}],\"edges\":[{\"from\":\"a\",\"to\":\"b\"},{\"from\":\"b\",\"to\":\"c\"},{\"from\":\"c\",\"to\":\"b\",\"label\":\"调整\"}]}}\n" +
		"```\n" +
		"\n" +
		"```chat-ui\n" +
		"{\"schema_version\":1,\"id\":\"wave-1\",\"type\":\"chart.line\",\"props\":{\"title\":\"正弦函数\",\"source\":{\"kind\":\"sine\",\"amplitude\":1,\"frequency\":1,\"duration\":2,\"controls\":[\"amplitude\",\"frequency\"]}}}\n" +
		"```\n" +
		"\n" +
		"表格、图形和公式只在本机绘制；这次演示没有调用真实模型。\n"
}
