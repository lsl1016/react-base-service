/**
 * Plan 模式 E2E 测试驱动：Node WS 客户端
 * 用法: node driver.js <scenario.json>
 * scenario.json 定义: { name, prompt, resumePolicy, maxWaitSec, ... }
 */
'use strict';
const WebSocket = require('C:/Users/keke/Desktop/xm/react-base-service/web/sdk/node_modules/ws');
const fs = require('fs');
const path = require('path');

const BASE = 'http://127.0.0.1:8180/react-base-service';
const WS_URL = 'ws://127.0.0.1:8180/react-base-service/react/ws';
const OUT_DIR = path.join(__dirname, 'artifacts');

class PlanClient {
  static _instances = [];
  constructor(tag) {
    this.tag = tag;
    this.events = [];          // 全量原始事件（去掉 heartbeat）
    this.views = [];           // plan_view_update 快照序列
    this.runId = null;
    this.sessionId = null;
    this.planExecutionId = null;
    this.done = false;
    this.donePayload = null;
    this.cancelled = false;
    this.errorEvent = null;
    this._waiters = [];
    PlanClient._instances.push(this);
  }

  connect() {
    return new Promise((resolve, reject) => {
      this.ws = new WebSocket(WS_URL, { headers: { 'X-User-Name': 'plan-e2e-tester' } });
      this.ws.on('open', resolve);
      this.ws.on('error', reject);
      this.ws.on('message', (raw) => this._onMessage(raw));
      this.ws.on('close', (code, reason) => {
        this.closed = true;
        this._notifyWaiters();
      });
    });
  }

  _onMessage(raw) {
    let ev;
    try { ev = JSON.parse(raw.toString()); } catch { return; }
    if (ev.type === 'heartbeat') return;
    this.events.push({ t: Date.now(), ...ev });
    if (ev.runId && !this.runId && ev.type !== 'error') this.runId = ev.runId;
    if (ev.sessionId && !this.sessionId) this.sessionId = ev.sessionId;
    if (ev.type === 'plan_view_update') {
      const view = ev.payload && ev.payload.view;
      if (view) {
        this.views.push(view);
        if (view.plan_execution_id) this.planExecutionId = view.plan_execution_id;
      }
    }
    if (ev.type === 'done') { this.done = true; this.donePayload = ev.payload || null; }
    if (ev.type === 'cancelled') { this.cancelled = true; this.donePayload = ev.payload || null; }
    if (ev.type === 'error') { this.errorEvent = ev; }
    this._notifyWaiters();
  }

  _notifyWaiters() {
    this._waiters = this._waiters.filter(w => {
      let ok;
      try { ok = w.check(this); } catch (e) { w.reject(e); return false; }
      if (ok) { w.resolve(); return false; }
      if (this.closed) { w.reject(new Error('ws closed')); return false; }
      return true;
    });
  }

  /** 轮询断言: check(client) 返回真值时 resolve */
  waitUntil(check, timeoutMs, label) {
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this._waiters = this._waiters.filter(w => w.check !== check);
        reject(new Error(`timeout(${timeoutMs}ms) waiting: ${label}`));
      }, timeoutMs);
      const wrap = {
        check,
        resolve: () => { clearTimeout(timer); resolve(); },
        reject: (e) => { clearTimeout(timer); reject(e); },
      };
      // 先查一次
      if (check(this)) { clearTimeout(timer); return resolve(); }
      this._waiters.push(wrap);
    });
  }

  send(type, payload, extra) {
    this.ws.send(JSON.stringify({ type, payload, ...extra }));
  }

  startRun(payload) {
    this.send('run', payload);
  }

  planResume(planExecutionId, waitRequestId, response) {
    this.send('plan_resume', { planExecutionId, waitRequestId, response });
  }

  planCancel(planExecutionId) {
    this.send('plan_cancel', { planExecutionId });
  }

  planSkip(planExecutionId, stepId) {
    this.send('plan_skip', { planExecutionId, stepId });
  }

  close() { try { this.ws.close(); } catch {} }

  latestView() { return this.views.length ? this.views[this.views.length - 1] : null; }

  summarizeEvents() {
    const counts = {};
    for (const ev of this.events) counts[ev.type] = (counts[ev.type] || 0) + 1;
    return counts;
  }
}

async function httpPost(pathname, body) {
  const res = await fetch(BASE + pathname, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-User-Name': 'plan-e2e-tester' },
    body: JSON.stringify(body),
  });
  const text = await res.text();
  let json; try { json = JSON.parse(text); } catch { json = { raw: text.slice(0, 500) }; }
  return { status: res.status, json };
}

const sleep = (ms) => new Promise(r => setTimeout(r, ms));

/** 步骤工具调用统计（从 plan_step_event 包装的原生事件里提取） */
function toolCallsOf(client) {
  const calls = [];
  for (const ev of client.events) {
    if (ev.type !== 'plan_step_event') continue;
    const inner = ev.payload && ev.payload.event;
    if (!inner) continue;
    if (inner.type === 'tool_use_start' && inner.payload) {
      calls.push({
        stepKey: inner.agentPath,
        tool: inner.payload.toolName,
        status: inner.payload.status,
      });
    }
  }
  return calls;
}

module.exports = { PlanClient, httpPost, sleep, OUT_DIR, toolCallsOf, fs, path };
