// 规则组(RuleGroup) 与规则(Rule) 管理 API（AC-22/29/39）。已与 T7 实测契约对齐。
//
// 后端 camelCase：
//   RuleGroup: { id,name,scope,groupIds,groups:[{id,name}],ruleCount }
//   Rule: { id,match,action,order }
import request from './request'

export function listRuleGroups() {
  return request.get('/rule-groups')
}

// 创建规则组。payload: { name, scope:'global'|'group', groupIds? }
export function createRuleGroup(payload) {
  return request.post('/rule-groups', payload)
}

export function updateRuleGroup(id, payload) {
  return request.put(`/rule-groups/${id}`, payload)
}

export function deleteRuleGroup(id) {
  return request.delete(`/rule-groups/${id}`)
}

// 规则组应用到分组（规则组维度，覆盖式）AC-29
export function setRuleGroupGroups(id, groupIds) {
  return request.put(`/rule-groups/${id}/groups`, { groupIds })
}

// ===== 规则组下的规则 =====

export function listRules(ruleGroupId) {
  return request.get(`/rule-groups/${ruleGroupId}/rules`)
}

// 创建规则。payload: { match, action, order }
export function createRule(ruleGroupId, payload) {
  return request.post(`/rule-groups/${ruleGroupId}/rules`, payload)
}

export function updateRule(ruleGroupId, ruleId, payload) {
  return request.put(`/rule-groups/${ruleGroupId}/rules/${ruleId}`, payload)
}

export function deleteRule(ruleGroupId, ruleId) {
  return request.delete(`/rule-groups/${ruleGroupId}/rules/${ruleId}`)
}

// 规则测试器（AC-39）：{ target, groupId } → { action, matchedRule, fromGroup, matched, sniffNote }
export function testRule(payload) {
  return request.post('/rule-groups/test', payload)
}
