import type { WorkspaceApi } from '../../lib/workspace-contract';
import { isSupported, manager } from './dusk';
import { TOOL_DEFS, executeTool } from './tools';

/** Fired after any tool run so open Files views re-list the directory. */
export const WORKSPACE_CHANGED = 'kali:workspace-changed';

export const workspace: WorkspaceApi = {
  isSupported,
  ready: async () => { await manager(); },
  tools: () => TOOL_DEFS,
  execute: async (call, opts) => {
    const result = await executeTool(call, opts);
    window.dispatchEvent(new CustomEvent(WORKSPACE_CHANGED, { detail: { tool: call.name } }));
    return result;
  },
};

export { isSupported, manager as bootWorkspace, bootPhase, onBootPhase, WORKSPACE_ROOT, HOME } from './dusk';
