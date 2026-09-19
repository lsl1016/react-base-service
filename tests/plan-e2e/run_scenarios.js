/**
 * Plan 模式 E2E 场景测试
 * 用法: node run_scenarios.js [scenarioName ...]
 */
'use strict';
const { PlanClient, httpPost, sleep, OUT_DIR, toolCallsOf, fs, path } = require('./driver');

const CALLER = 'demo-app';
const RESULTS = [];
const ONLY = process.argv.slice(2);

function assert(cond, msg) {
  if (!cond) throw new Error(`ASSERT FAIL: ${msg}`);
  return true;
}

function runPayload(prompt) {
  return {
    callerKey: CALLER, type: 'chat', userPrompt: prompt, executionMode: 'plan',
    modelKey: 'claude', modelVersion: 'glm-4.6',
  };
}

/** 取 wait 请求并按 schema 构造响应 */
function buildUserInputResponse(view, overrides) {
  const wait = view.wait_request;
  assert(wait, 'view.wait_request 存在');
  const schema = wait.response_schema || {};
  const required = Array.isArray(schema.required) ? schema.required : [];
  const resp = {};
  for (const field of required) resp[field] = 'answer-for-field-' + field;
  const props = (schema.properties && typeof schema.properties === 'object') ? schema.properties : {};
  for (const [k, def] of Object.entries(props)) {
    if (resp[k] !== undefined) {
      if (def && def.type === 'number') resp[k] = 15;
      else if (def && def.type === 'boolean') resp[k] = true;
    }
  }
  return Object.assign(resp, overrides || {});
}

async function scenarioS1() {
  const c = new PlanClient('S1');
  await c.connect();
  const t0 = Date.now();
  c.startRun(runPayload(
    '请分析 adk-go 仓库，严格按以下三步执行：' +
    '1）只调用一次 repo_get_repo_map 获取 adk-go 的整体结构（不要调用其他检索工具、不要逐目录展开）；' +
    '2）基于步骤 1 的结果，用 python_exec 做统计：把步骤 1 摘要里出现的目录名和模块数量直接写成 Python 字面量（不要通过引用或文件传参），输出一张简单的统计表；' +
    '3）用中文总结该仓库的定位与架构特点（200 字以内）。每步都要快速完成，不要过度展开。'
  ));

  await c.waitUntil(cl => cl.done || cl.cancelled || cl.errorEvent, 1800000, 'plan run done');
  assert(c.done, 'S1 应正常 done 而非 error: ' + JSON.stringify(c.errorEvent));
  const finalView = c.latestView();
  assert(finalView, '收到 plan_view_update');
  assert(finalView.status === 'SUCCEEDED', `终态 SUCCEEDED，实际 ${finalView.status}`);
  assert(finalView.steps && finalView.steps.length >= 3, `步骤数>=3，实际 ${finalView.steps.length}`);
  const tools = toolCallsOf(c);
  const toolNames = [...new Set(tools.map(t => t.tool))];
  assert(tools.length >= 3, `工具调用>=3 次，实际 ${tools.length}`);
  const usedRepo = toolNames.some(n => n.startsWith('repo_') || n.startsWith('ws_'));
  const usedPython = toolNames.some(n => n === 'python_exec');
  assert(usedRepo && usedPython, `同时使用代码检索与 python_exec，实际工具集: ${toolNames.join(',')}`);
  const allSucc = finalView.steps.every(s => s.status === 'SUCCEEDED');
  assert(allSucc, '全部步骤 SUCCEEDED');

  // detail / events 接口验证
  const detail = await httpPost('/react/plan_execution/detail', {
    planExecutionId: finalView.plan_execution_id, sessionId: c.sessionId, callerKey: CALLER,
  });
  assert(detail.status === 200 && detail.json.errNo === 0, `detail 接口成功: ${JSON.stringify(detail.json).slice(0, 200)}`);
  const attempts = detail.json.data.attempts || [];
  assert(attempts.length === finalView.steps.length, `attempt 数与步骤数一致 (${attempts.length} vs ${finalView.steps.length})`);
  const succAttempts = attempts.filter(a => a.status === 'SUCCEEDED');
  if (succAttempts.length) {
    const ev = await httpPost('/react/plan_execution/events', {
      planExecutionId: finalView.plan_execution_id, stepAttemptId: succAttempts[0].stepAttemptId,
      sessionId: c.sessionId, callerKey: CALLER,
    });
    assert(ev.status === 200 && ev.json.errNo === 0, 'step events 接口成功');
    const evs = (ev.json.data && ev.json.data.events) || [];
    assert(evs.length > 0, `attempt 事件可重建 (${evs.length} 条)`);
  }
  c.close();
  return {
    name: 'S1-复杂正常流(检索+沙箱计算)',
    pass: true, ms: Date.now() - t0, session: c.sessionId, plan: finalView.plan_execution_id,
    steps: finalView.steps.map(s => ({ key: s.step_id, type: s.step_type, status: s.status })),
    tools: toolNames, toolCallCount: tools.length,
    finalSummary: (finalView.result && (finalView.result.summary || finalView.result.text)) || '',
    eventCounts: c.summarizeEvents(),
  };
}

async function scenarioS2() {
  const c = new PlanClient('S2');
  await c.connect();
  const t0 = Date.now();
  c.startRun(runPayload(
    '我想生成一份定制化的技术学习路线报告，但在开始之前必须先确认我的技术背景。' +
    '请把「询问用户最熟悉的编程语言与从业年限」安排为一个 USER_INPUT 步骤放在计划开头（步骤类型必须是 USER_INPUT），' +
    '拿到我的回答后，再用 python_exec 工具生成一份对比学习路线表（含每周时间投入估算），最后输出中文总结报告。'
  ));

  // 等待 USER_INPUT 等待态
  await c.waitUntil(cl => {
    if (cl.errorEvent) throw new Error('run error: ' + JSON.stringify(cl.errorEvent.payload));
    const v = cl.latestView();
    return v && (v.status === 'WAIT_USER_INPUT' || v.status === 'WAITING') && v.wait_request;
  }, 600000, 'WAIT_USER_INPUT');
  const waitView = c.latestView();
  const userInputStep = waitView.steps.find(s => s.step_type === 'USER_INPUT');
  assert(userInputStep, '计划中存在 USER_INPUT 步骤');
  assert(waitView.wait_request.request_id, 'wait_request.request_id 存在');

  // 并发互斥: 等待期间同会话发普通 react run 应被拒
  const c2 = new PlanClient('S2-mutex');
  await c2.connect();
  const mutexResult = { rejected: false, errMsg: '' };
  c2.startRun({ callerKey: CALLER, type: 'chat', userPrompt: '普通消息并发测试', sessionId: c.sessionId });
  try {
    await c2.waitUntil(cl => !!cl.errorEvent, 30000, '并发 run 被拒');
    mutexResult.rejected = true;
    mutexResult.errMsg = (c2.errorEvent.payload && c2.errorEvent.payload.errMsg) || '';
  } catch { mutexResult.rejected = false; }
  c2.close();
  assert(mutexResult.rejected, '等待期间普通 run 被拒绝（waiting_plan 互斥）');

  // WS 恢复
  const resp = buildUserInputResponse(waitView, {
    answer: '我最熟悉 Go，已从业 5 年，每周可投入 8 小时学习时间',
  });
  c.planResume(waitView.plan_execution_id, waitView.wait_request.request_id, resp);
  await c.waitUntil(cl => cl.done || cl.cancelled || cl.errorEvent, 900000, 'resume 后 run done');
  assert(c.done, 'S2 恢复后应 done: ' + JSON.stringify(c.errorEvent));
  const finalView = c.latestView();
  assert(finalView.status === 'SUCCEEDED', `终态 SUCCEEDED，实际 ${finalView.status}`);
  const answered = finalView.steps.find(s => s.step_type === 'USER_INPUT');
  assert(answered && answered.status === 'SUCCEEDED', 'USER_INPUT 步骤最终 SUCCEEDED');
  c.close();
  return {
    name: 'S2-USER_INPUT等待+WS恢复+并发互斥',
    pass: true, ms: Date.now() - t0, session: c.sessionId, plan: finalView.plan_execution_id,
    steps: finalView.steps.map(s => ({ key: s.step_id, type: s.step_type, status: s.status })),
    mutexCheck: mutexResult, eventCounts: c.summarizeEvents(),
  };
}

async function scenarioS3() {
  const c = new PlanClient('S3');
  await c.connect();
  const t0 = Date.now();
  c.startRun(runPayload(
    '本任务包含一个高风险动作：清理一批历史临时数据文件。请这样制定计划：' +
    '1）先用 python_exec 工具生成（仅生成，不删除）一份模拟的待清理文件清单（10 行左右，含文件名与大小）；' +
    '2）把「请求用户确认是否真正执行删除」安排为一个 USER_ACTION 步骤（步骤类型必须是 USER_ACTION）；' +
    '3）若用户批准才会执行清理（本次测试用户会拒绝，因此清理步骤不应真正执行）。'
  ));

  await c.waitUntil(cl => {
    if (cl.errorEvent) throw new Error('run error: ' + JSON.stringify(cl.errorEvent.payload));
    const v = cl.latestView();
    return v && v.status === 'WAIT_USER_ACTION' && v.wait_request;
  }, 600000, 'WAIT_USER_ACTION');
  const waitView = c.latestView();
  const actionStep = waitView.steps.find(s => s.step_type === 'USER_ACTION');
  assert(actionStep, '计划中存在 USER_ACTION 步骤');

  // 拒绝 → 整体取消
  c.planResume(waitView.plan_execution_id, waitView.wait_request.request_id, { approved: false });
  await c.waitUntil(cl => cl.done || cl.cancelled || cl.errorEvent, 300000, '拒绝后 run 结束');
  const finalView = c.latestView();
  assert(finalView.status === 'CANCELLED', `拒绝后 Plan 应 CANCELLED，实际 ${finalView.status}`);
  const statuses = finalView.steps.map(s => `${s.step_id}:${s.status}`);
  c.close();
  return {
    name: 'S3-USER_ACTION拒绝→级联取消',
    pass: true, ms: Date.now() - t0, session: c.sessionId, plan: finalView.plan_execution_id,
    steps: finalView.steps.map(s => ({ key: s.step_id, type: s.step_type, status: s.status })),
    stepStatusChain: statuses.join(', '), eventCounts: c.summarizeEvents(),
  };
}

async function scenarioS4() {
  const c = new PlanClient('S4');
  await c.connect();
  const t0 = Date.now();
  c.startRun(runPayload(
    '请制定一个多步骤纯计算任务，每个计算步骤都要调用 python_exec 工具：' +
    '1）计算 1000 以内的素数个数与总和；2）计算斐波那契数列第 40 项的值；' +
    '3）用蒙特卡洛方法（10 万次采样）估算圆周率；4）汇总以上三项结果输出中文报告。'
  ));

  // 等第一个 AGENT 步骤进入 RUNNING
  await c.waitUntil(cl => {
    if (cl.errorEvent) throw new Error('run error: ' + JSON.stringify(cl.errorEvent.payload));
    const v = cl.latestView();
    return v && v.current_step && v.current_step.status === 'RUNNING';
  }, 600000, '首个 AGENT 步骤 RUNNING');
  const midView = c.latestView();

  // 副本检查: WS plan_cancel 在 run 活跃期应被控制器拒绝（等待态才接受）
  const wsCancelProbe = { rejected: false, errMsg: '' };
  const before = c.events.length;
  c.planCancel(midView.plan_execution_id);
  await sleep(3000);
  const errEv = c.events.slice(before).find(e => e.type === 'error');
  if (errEv) { wsCancelProbe.rejected = true; wsCancelProbe.errMsg = errEv.payload && errEv.payload.errMsg; }

  // 运行中取消: 走 HTTP 通道
  const http = await httpPost('/react/plan_execution/cancel', { planExecutionId: midView.plan_execution_id });
  assert(http.status === 200 && http.json.errNo === 0, `HTTP cancel 成功: ${JSON.stringify(http.json).slice(0, 200)}`);

  await c.waitUntil(cl => cl.done || cl.cancelled || cl.errorEvent, 300000, '取消后 run 结束');
  const finalView = c.latestView();
  assert(finalView.status === 'CANCELLED', `取消后终态 CANCELLED，实际 ${finalView.status}`);
  const notTerminal = finalView.steps.filter(s => !['CANCELLED', 'SUCCEEDED', 'SKIPPED', 'FAILED'].includes(s.status));
  assert(notTerminal.length === 0, `全部步骤应达终态，异常: ${notTerminal.map(s => s.step_id + ':' + s.status).join(',')}`);
  c.close();
  return {
    name: 'S4-运行中取消(HTTP)+WS拒绝副本',
    pass: true, ms: Date.now() - t0, session: c.sessionId, plan: finalView.plan_execution_id,
    steps: finalView.steps.map(s => ({ key: s.step_id, type: s.step_type, status: s.status })),
    wsCancelProbe, eventCounts: c.summarizeEvents(),
  };
}

async function scenarioS5() {
  const c = new PlanClient('S5');
  await c.connect();
  const t0 = Date.now();
  c.startRun(runPayload(
    '请帮我生成一份「服务器容量规划」建议报告。计划中必须先安排一个 USER_INPUT 步骤询问我当前的日均请求量与峰值 QPS，' +
    '获得我的输入后，用 python_exec 工具按 30% 年增长做容量推演并输出估算表，最后给出中文结论。'
  ));

  await c.waitUntil(cl => {
    if (cl.errorEvent) throw new Error('run error: ' + JSON.stringify(cl.errorEvent.payload));
    const v = cl.latestView();
    return v && v.status === 'WAIT_USER_INPUT' && v.wait_request;
  }, 600000, 'WAIT_USER_INPUT');
  const waitView = c.latestView();
  c.close(); // 关闭 WS，验证不依赖原连接

  const resp = buildUserInputResponse(waitView, { answer: '日均请求量 2000 万，峰值 QPS 1500' });
  const http = await httpPost('/react/plan_execution/resume', {
    planExecutionId: waitView.plan_execution_id, waitRequestId: waitView.wait_request.request_id, response: resp,
  });
  assert(http.status === 200 && http.json.errNo === 0, `HTTP resume 成功: ${JSON.stringify(http.json).slice(0, 300)}`);

  // 轮询 detail 接口直到终态
  let finalView = null;
  for (let i = 0; i < 90; i++) {
    await sleep(10000);
    const d = await httpPost('/react/plan_execution/detail', {
      planExecutionId: waitView.plan_execution_id, sessionId: c.sessionId, callerKey: CALLER,
    });
    assert(d.status === 200 && d.json.errNo === 0, 'detail 接口可用');
    finalView = d.json.data.view;
    if (['SUCCEEDED', 'FAILED', 'CANCELLED'].includes(finalView.status)) break;
  }
  assert(finalView && finalView.status === 'SUCCEEDED', `HTTP 恢复后终态 SUCCEEDED，实际 ${finalView && finalView.status}`);
  const tools = toolCallsOf(c); // WS 关闭前的事件
  return {
    name: 'S5-HTTP通道恢复(断开WS)',
    pass: true, ms: Date.now() - t0, session: c.sessionId, plan: finalView.plan_execution_id,
    steps: finalView.steps.map(s => ({ key: s.step_id, type: s.step_type, status: s.status })),
    wsClosedBeforeResume: true, eventCountsBeforeClose: c.summarizeEvents(),
  };
}

async function scenarioS6() {
  const c = new PlanClient('S6');
  await c.connect();
  const t0 = Date.now();
  c.startRun(runPayload(
    '请帮我制定一份阅读计划。计划第一步必须是一个 USER_INPUT 步骤（步骤类型必须是 USER_INPUT），询问我每月的阅读时长偏好，' +
    '拿到回答后用 python_exec 计算一年可读完的书籍数量（按每本 300 页估算），输出简短中文结论。'
  ));

  await c.waitUntil(cl => {
    if (cl.errorEvent) throw new Error('run error: ' + JSON.stringify(cl.errorEvent.payload));
    const v = cl.latestView();
    return v && v.status === 'WAIT_USER_INPUT' && v.wait_request;
  }, 600000, 'WAIT_USER_INPUT');
  const waitView = c.latestView();

  // WAIT 态并发互斥（正确 modelKey 探针）
  const c2 = new PlanClient('S6-mutex');
  await c2.connect();
  const probe = { rejected: false, errMsg: '' };
  c2.startRun({
    callerKey: CALLER, type: 'chat', userPrompt: 'WAIT 态并发互斥探针',
    sessionId: c.sessionId, modelKey: 'claude', modelVersion: 'glm-4.6',
  });
  try {
    await c2.waitUntil(cl => !!cl.errorEvent, 30000, 'WAIT 态并发被拒');
    probe.rejected = true;
    probe.errMsg = (c2.errorEvent.payload && c2.errorEvent.payload.errMsg) || '';
  } catch { /* 未被拒则探针会超时 */ }
  c2.close();
  assert(/运行中|active|run/i.test(probe.errMsg), `拒绝原因为互斥而非参数错误: ${probe.errMsg}`);

  // 恢复完成
  const resp = buildUserInputResponse(waitView, { answer: '每月大约 20 小时' });
  c.planResume(waitView.plan_execution_id, waitView.wait_request.request_id, resp);
  await c.waitUntil(cl => cl.done || cl.cancelled || cl.errorEvent, 900000, 'resume 后 done');
  assert(c.done, 'S6 恢复后应 done: ' + JSON.stringify(c.errorEvent));
  const finalView = c.latestView();
  assert(finalView.status === 'SUCCEEDED', `终态 SUCCEEDED，实际 ${finalView.status}`);
  c.close();
  return {
    name: 'S6-WAIT态互斥复测+恢复',
    pass: true, ms: Date.now() - t0, session: c.sessionId, plan: finalView.plan_execution_id,
    steps: finalView.steps.map(s => ({ key: s.step_id, type: s.step_type, status: s.status })),
    mutexCheck: probe,
  };
}

const SCENARIOS = {
  s1: scenarioS1, s2: scenarioS2, s3: scenarioS3, s4: scenarioS4, s5: scenarioS5, s6: scenarioS6,
};

async function main() {
  fs.mkdirSync(OUT_DIR, { recursive: true });
  const names = ONLY.length ? ONLY : Object.keys(SCENARIOS);
  for (const key of names) {
    const fn = SCENARIOS[key];
    console.log(`\n===== ${key} ${new Date().toISOString()} =====`);
    const t = Date.now();
    try {
      const r = await fn();
      RESULTS.push(r);
      console.log(`PASS ${r.name} (${((Date.now() - t) / 1000).toFixed(1)}s)`);
    } catch (e) {
      RESULTS.push({ name: key, pass: false, error: e.message, ms: Date.now() - t });
      console.log(`FAIL ${key}: ${e.message}`);
    }
  }
  // 落盘各客户端事件原始流（driver 的全局登记）
  for (const [i, client] of PlanClient._instances.entries()) {
    fs.writeFileSync(path.join(OUT_DIR, `events-${i}-${client.tag}.json`), JSON.stringify(client.events, null, 1));
  }
  fs.writeFileSync(path.join(OUT_DIR, 'results.json'), JSON.stringify(RESULTS, null, 2));
  console.log('\n===== SUMMARY =====');
  for (const r of RESULTS) console.log(`${r.pass ? 'PASS' : 'FAIL'}  ${r.name}${r.pass ? '' : ' :: ' + r.error}`);
  process.exit(RESULTS.every(r => r.pass) ? 0 : 1);
}

main().catch(e => { console.error('FATAL', e); process.exit(2); });
