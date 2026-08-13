## subagent实现功能规划

1. 目标：实现一个可被主 agent 调用的子 agent，支持并行执行和独立状态管理。
2. 子agent完整生命周期的控制
   - 创建：主 agent可以创建子 agent实例，并传递必要的初始化参数。
   - 执行：子 agent可以独立执行任务，支持并行处理。
   - 状态管理：子 agent维护自己的状态，包括会话信息、工具使用记录等。
   - 销毁：主 agent可以销毁子 agent实例，释放资源。
3. subagent一样是无状态引擎。
4. subagent的会话记录需要落盘，需要看看具体落盘位置好一点
5. 支持主agent消息通知subagent，subagent可以向主agent发送消息（待考量实现难度和是否有必要）。
6. 支持subagent的中断，resume
7. subagent的实现方式：协程，还是fork进程，还是线程池？（待讨论）
8. subagent的工具系统：是否和主agent共享工具，还是独立工具系统？（待讨论）
9. subagent的权限系统：是否和主agent共享权限，还是独立权限系统？（待讨论）
10. subagent的配置系统：是否和主agent共享配置，还是独立配置系统？（待讨论）
11. subagent的状态要挂在哪里？session吗？还是sessionmanager？
12. tui上的subagent管理界面：是否需要在tui上提供subagent的管理界面，方便查看和控制子agent的状态和任务执行情况？（待讨论）
13. 是否支持用户主动给subagent发送消息？
14. 如何处理subagent的工具调用？