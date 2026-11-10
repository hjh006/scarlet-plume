from django.conf import settings
from django.db import models


class Workspace(models.Model):
    slug = models.SlugField(max_length=60, unique=True)
    name = models.CharField("工作区名称", max_length=100)

    def __str__(self):
        return self.name


class Membership(models.Model):
    class Role(models.TextChoices):
        OWNER = "owner", "负责人"
        OPERATOR = "operator", "经办人"
        REVIEWER = "reviewer", "复核人"
        VIEWER = "viewer", "只读成员"

    workspace = models.ForeignKey(Workspace, on_delete=models.CASCADE)
    user = models.ForeignKey(settings.AUTH_USER_MODEL, on_delete=models.CASCADE)
    role = models.CharField(max_length=12, choices=Role.choices)

    class Meta:
        constraints = [
            models.UniqueConstraint(fields=["workspace", "user"], name="unique_workspace_member"),
        ]

    @property
    def can_edit(self):
        return self.role in (self.Role.OWNER, self.Role.OPERATOR)

    @property
    def can_review(self):
        return self.role in (self.Role.OWNER, self.Role.REVIEWER)


class Requirement(models.Model):
    code = models.CharField(max_length=12, unique=True)
    title = models.CharField(max_length=100)
    citation = models.CharField(max_length=240)
    source_url = models.URLField(max_length=500)
    summary = models.TextField(max_length=800)

    class Meta:
        ordering = ["code"]

    def __str__(self):
        return f"{self.code} · {self.title}"


class Asset(models.Model):
    class Category(models.TextChoices):
        UNKNOWN = "unknown", "待确认"
        BUSINESS = "business", "业务数据"
        PERSONAL = "personal", "个人信息"
        SENSITIVE_PERSONAL = "sensitive_personal", "敏感个人信息"

    class BusinessLevel(models.TextChoices):
        UNKNOWN = "unknown", "待确认"
        INTERNAL = "internal", "内部"
        CONFIDENTIAL = "confidential", "机密"

    class StatutoryCategory(models.TextChoices):
        UNKNOWN = "unknown", "待确认"
        GENERAL = "general", "一般数据"
        IMPORTANT = "important", "重要数据"
        CORE = "core", "核心数据"

    workspace = models.ForeignKey(Workspace, on_delete=models.CASCADE)
    name = models.CharField("资产名称", max_length=100)
    system = models.CharField("来源系统", max_length=100)
    category = models.CharField("数据属性", max_length=20, choices=Category.choices, default=Category.UNKNOWN)
    business_level = models.CharField(
        "企业内部敏感级别", max_length=15, choices=BusinessLevel.choices, default=BusinessLevel.UNKNOWN,
    )
    statutory_category = models.CharField(
        "法定数据类别", max_length=12, choices=StatutoryCategory.choices, default=StatutoryCategory.UNKNOWN,
    )
    classification_basis = models.CharField("分级依据", max_length=400, blank=True)
    owner_team = models.CharField("责任团队", max_length=80)
    description = models.TextField("说明", max_length=600, blank=True)
    created_at = models.DateTimeField(auto_now_add=True)

    class Meta:
        ordering = ["-created_at", "-pk"]
        constraints = [
            models.UniqueConstraint(fields=["workspace", "system", "name"], name="unique_workspace_asset"),
        ]

    def __str__(self):
        return f"{self.system} · {self.name}"


class Activity(models.Model):
    class Kind(models.TextChoices):
        INTERNAL = "internal", "内部处理"
        SHARING = "sharing", "对外共享或委托"
        AI = "ai", "AI 使用"

    # Tenant scope comes from the asset, so it cannot drift from the activity.
    asset = models.ForeignKey(Asset, on_delete=models.PROTECT)
    name = models.CharField("活动名称", max_length=100)
    kind = models.CharField("活动类型", max_length=12, choices=Kind.choices)
    purpose = models.CharField("处理目的", max_length=400)
    recipient = models.CharField("接收方或模型服务", max_length=100, blank=True)
    region = models.CharField("处理地域", max_length=100, blank=True)
    retention_policy = models.CharField("留存和训练条款", max_length=400, blank=True)
    created_at = models.DateTimeField(auto_now_add=True)

    class Meta:
        ordering = ["-created_at", "-pk"]


class Check(models.Model):
    class Status(models.TextChoices):
        DRAFT = "draft", "待取证"
        IN_REVIEW = "in_review", "待复核"
        NEEDS_WORK = "needs_work", "待整改"
        VERIFIED = "verified", "已验证"

    asset = models.ForeignKey(Asset, on_delete=models.PROTECT)
    requirement = models.ForeignKey(Requirement, on_delete=models.PROTECT)
    owner_team = models.CharField("整改责任团队", max_length=80)
    note = models.TextField("检查范围与标准", max_length=1000)
    status = models.CharField(max_length=12, choices=Status.choices, default=Status.DRAFT)
    created_by = models.ForeignKey(settings.AUTH_USER_MODEL, on_delete=models.PROTECT, related_name="+")
    submitted_by = models.ForeignKey(
        settings.AUTH_USER_MODEL, on_delete=models.PROTECT, related_name="+", null=True,
    )
    created_at = models.DateTimeField(auto_now_add=True)
    updated_at = models.DateTimeField(auto_now=True)

    class Meta:
        ordering = ["-updated_at", "-pk"]

    def can_review_with(self, membership):
        return (
            membership.can_review
            and self.status == self.Status.IN_REVIEW
            and membership.user_id not in (self.created_by_id, self.submitted_by_id)
            and not self.evidence.filter(created_by_id=membership.user_id).exists()
        )


class Evidence(models.Model):
    class Kind(models.TextChoices):
        OBSERVATION = "observation", "检查记录"
        REMEDIATION = "remediation", "整改记录"
        RETEST = "retest", "复测记录"

    control_check = models.ForeignKey(Check, on_delete=models.CASCADE, related_name="evidence")
    kind = models.CharField("证据类型", max_length=12, choices=Kind.choices)
    source_system = models.CharField("证据来源系统", max_length=100)
    source_record = models.CharField("原始记录引用", max_length=160)
    summary = models.TextField("脱敏证据摘要", max_length=600)
    observed_at = models.DateTimeField("证据发生时间")
    created_by = models.ForeignKey(settings.AUTH_USER_MODEL, on_delete=models.PROTECT, related_name="+")
    created_at = models.DateTimeField(auto_now_add=True)

    class Meta:
        ordering = ["-created_at", "-pk"]


class CheckEvent(models.Model):
    class Action(models.TextChoices):
        CREATED = "created", "创建检查"
        EVIDENCE = "evidence", "登记证据"
        SUBMITTED = "submitted", "提交复核"
        CHANGES = "changes", "要求整改"
        VERIFIED = "verified", "复核通过"

    control_check = models.ForeignKey(Check, on_delete=models.CASCADE, related_name="events")
    action = models.CharField(max_length=12, choices=Action.choices)
    actor = models.ForeignKey(settings.AUTH_USER_MODEL, on_delete=models.PROTECT, related_name="+")
    note = models.CharField(max_length=600, blank=True)
    created_at = models.DateTimeField(auto_now_add=True)

    class Meta:
        ordering = ["-created_at", "-pk"]
