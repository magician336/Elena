# Elena

Agent 原生的嵌入式状态存储:为 Agent 会话、轨迹与检查点提供持久化底座。本文件收录项目专属术语;通用编程概念不收录。

## Language

**轨迹 (Trajectory)**:
一个会话内按发生顺序追加的事件序列;不可变、只增不改。
_Avoid_: trace(与 flamingos 域的 OTel trace 混淆)、history、log

**会话 (Session)**:
agent 交互的一段连续边界单位,唯一归属一个轨迹,带生命周期(TTL)。
_Avoid_: conversation、context(与"上下文工程"话术混淆)

**事件 (Event)**:
轨迹中的一条不可变追加记录,是写入的最小单位。
_Avoid_: record、entry

**命名空间 (Namespace)**:
会话与数据的隔离与组织层级。
_Avoid_: tenant(明确不做多租户)、prefix

**检查点 (Checkpoint)**:
对某一时间点状态的持久化快照,是恢复的基准点。
_Avoid_: snapshot、savepoint

**时间点读 (Point-in-time Read)**:
读取轨迹中至某一序号为止全部内容的查询。
_Avoid_: replay、回放(指演示动作,不作查询名)

**合并 (Merge)**:
Bitcask 模型下回收无效记录、重写数据文件的过程。
_Avoid_: compaction(保留给 LSM 语境,MVP 不涉及;两词不混用)、GC

**语义层 (Semantic Layer)**:
引擎之上以会话/轨迹/检查点表达的 agent 概念 API 层。
_Avoid_: SDK、framework、中间件
