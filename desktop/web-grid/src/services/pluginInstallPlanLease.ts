import type { PluginInstallPlan } from "@/contracts";

export interface PluginProjectContext {
  readonly ready: boolean;
  readonly projectKey: string;
  readonly projectRevision: string;
  readonly generation: number;
}

export interface PluginInstallInspection {
  readonly id: number;
  readonly context: PluginProjectContext;
}

export interface PluginInstallInspectionStart {
  readonly inspection: PluginInstallInspection;
  readonly releasedPlan: PluginInstallPlan | null;
}

/** Owns inspect admission, replacement, invalidation and one-shot consumption. */
export class PluginInstallPlanLease {
  private nextInspectionId = 0;
  private owner: PluginInstallInspection | null = null;
  private currentPlan: PluginInstallPlan | null = null;

  get plan(): PluginInstallPlan | null {
    return this.currentPlan;
  }

  begin(context: PluginProjectContext): PluginInstallInspectionStart {
    assertReady(context);
    const releasedPlan = this.takePlan();
    const inspection = { id: ++this.nextInspectionId, context: { ...context } };
    this.owner = inspection;
    return { inspection, releasedPlan };
  }

  admit(
    inspection: PluginInstallInspection,
    context: PluginProjectContext,
    plan: PluginInstallPlan,
  ): boolean {
    if (!this.owns(inspection) || !sameContext(inspection.context, context)
      || plan.projectKey !== inspection.context.projectKey
      || plan.projectRevision !== inspection.context.projectRevision) return false;
    this.currentPlan = plan;
    return true;
  }

  fail(inspection: PluginInstallInspection): boolean {
    if (!this.owns(inspection)) return false;
    this.owner = null;
    return true;
  }

  invalidate(): PluginInstallPlan | null {
    this.owner = null;
    this.nextInspectionId += 1;
    return this.takePlan();
  }

  consume(requested: PluginInstallPlan, context: PluginProjectContext): PluginInstallPlan {
    if (this.currentPlan?.planId !== requested.planId) {
      throw new PluginInstallPlanStaleError();
    }
    const inspection = this.owner;
    const plan = this.takePlan()!;
    this.owner = null;
    if (!inspection || !sameContext(inspection.context, context)
      || !samePlanContext(plan, context)) throw new PluginInstallPlanStaleError();
    return plan;
  }

  release(planId: string): PluginInstallPlan | null {
    if (this.currentPlan?.planId !== planId) return null;
    this.owner = null;
    return this.takePlan();
  }

  owns(inspection: PluginInstallInspection): boolean {
    return this.owner?.id === inspection.id;
  }

  private takePlan(): PluginInstallPlan | null {
    const plan = this.currentPlan;
    this.currentPlan = null;
    return plan;
  }
}

export class PluginProjectNotReadyError extends Error {
  constructor() {
    super("当前工作区尚未就绪，无法管理插件。");
    this.name = "PluginProjectNotReadyError";
  }
}

export class PluginInstallPlanStaleError extends Error {
  constructor() {
    super("插件安装计划已失效，请在当前工作区重新检查");
    this.name = "PluginInstallPlanStaleError";
  }
}

function assertReady(context: PluginProjectContext): void {
  if (!context.ready || !context.projectKey || !context.projectRevision) {
    throw new PluginProjectNotReadyError();
  }
}

function sameContext(left: PluginProjectContext, right: PluginProjectContext): boolean {
  return left.ready && right.ready
    && left.projectKey === right.projectKey
    && left.projectRevision === right.projectRevision
    && left.generation === right.generation;
}

function samePlanContext(plan: PluginInstallPlan, context: PluginProjectContext): boolean {
  return context.ready
    && plan.projectKey === context.projectKey
    && plan.projectRevision === context.projectRevision;
}
