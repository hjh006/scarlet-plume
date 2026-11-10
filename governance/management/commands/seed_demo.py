from django.conf import settings
from django.core.management.base import BaseCommand, CommandError
from django.db import transaction
from django.utils import timezone

from governance.models import Activity, Asset, Check, CheckEvent, Evidence, Membership, Requirement, Workspace


class Command(BaseCommand):
    help = "在空工作区载入合成演示数据（开发模式）。"

    def add_arguments(self, parser):
        parser.add_argument("slug")

    @transaction.atomic
    def handle(self, *args, **options):
        if not settings.DEBUG:
            raise CommandError("演示数据只可在开发模式载入。")
        workspace = Workspace.objects.select_for_update().filter(slug=options["slug"]).first()
        if workspace is None:
            raise CommandError("请先用 provision_workspace 创建工作区。")
        if Asset.objects.filter(workspace=workspace).exists():
            raise CommandError("演示命令只用于尚未登记资产的工作区。")
        members = Membership.objects.filter(workspace=workspace).select_related("user")
        editor = members.filter(role__in=(Membership.Role.OWNER, Membership.Role.OPERATOR)).first()
        if editor is None:
            raise CommandError("工作区需要一名经办人或负责人。")
        reviewer = members.filter(role__in=(Membership.Role.OWNER, Membership.Role.REVIEWER)).exclude(
            user_id=editor.user_id,
        ).first()

        sample = [
            ("售后工单数据", "服务工单系统", "售后服务部", "T01", "复核供应商共享账号、用途和到期日。"),
            ("研发项目目录", "研发资料库", "研发运营组", "G02", "核对数据目录、内部敏感级别与归属团队。"),
            ("应用访问记录", "日志审计系统", "安全运营组", "G04", "抽查相关网络日志的留存策略与最早可查日期。"),
            ("知识助手样例", "内部 AI 沙箱", "数字化团队", "X01", "确认送模内容、实际处理地域及供应商条款。"),
        ]
        assets = []
        for index, (name, system, owner, code, note) in enumerate(sample):
            asset = Asset.objects.create(
                workspace=workspace, name=name, system=system, owner_team=owner,
                category=Asset.Category.PERSONAL if index == 0 else Asset.Category.BUSINESS,
                business_level=Asset.BusinessLevel.INTERNAL,
                description="合成演示记录，可用于体验流程。",
            )
            assets.append(asset)
            check = Check.objects.create(
                asset=asset, requirement=Requirement.objects.get(code=code), owner_team=owner,
                note=note, created_by=editor.user,
            )
            check.events.create(action=CheckEvent.Action.CREATED, actor=editor.user)
            if index == 3:
                continue
            check.evidence.create(
                kind=Evidence.Kind.OBSERVATION, source_system=system, source_record=f"DEMO-{index + 1:03}",
                summary="合成记录。源系统中的证据引用用于复核，当前摘要只包含演示信息。",
                observed_at=timezone.now(), created_by=editor.user,
            )
            check.events.create(action=CheckEvent.Action.EVIDENCE, actor=editor.user, note="检查记录")
            check.status = Check.Status.IN_REVIEW
            check.submitted_by = editor.user
            check.events.create(action=CheckEvent.Action.SUBMITTED, actor=editor.user)
            if reviewer is not None and index in (1, 2):
                check.status = Check.Status.NEEDS_WORK if index == 1 else Check.Status.VERIFIED
                check.events.create(
                    action=CheckEvent.Action.CHANGES if index == 1 else CheckEvent.Action.VERIFIED,
                    actor=reviewer.user,
                    note="补充样例目录确认记录。" if index == 1 else "合成检查记录与留存策略匹配。",
                )
            check.save(update_fields=["status", "submitted_by", "updated_at"])

        Activity.objects.create(
            asset=assets[0], name="服务商工单共享", kind=Activity.Kind.SHARING,
            purpose="在指定工单范围内开展售后协同", recipient="示例维修服务商",
            region="中国大陆", retention_policy="按演示协议约定期限保留",
        )
        Activity.objects.create(
            asset=assets[3], name="内部知识助手", kind=Activity.Kind.AI,
            purpose="用合成样本验证内部知识检索", recipient="内部 AI 沙箱",
            region="中国大陆", retention_policy="仅合成样本，不用于训练",
        )
        self.stdout.write(self.style.SUCCESS("已创建 4 项资产、4 项控制检查和 2 项处理活动。"))
