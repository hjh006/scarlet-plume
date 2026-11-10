from datetime import timedelta

from django.contrib.auth import get_user_model
from django.test import TestCase
from django.urls import reverse
from django.utils import timezone

from .models import Activity, Asset, Check, CheckEvent, Evidence, Membership, Requirement, Workspace


class GovernanceTests(TestCase):
    @classmethod
    def setUpTestData(cls):
        cls.workspace = Workspace.objects.create(slug="alpha", name="示例工作区甲")
        cls.other_workspace = Workspace.objects.create(slug="beta", name="示例工作区乙")
        cls.owner = cls.create_member(cls.workspace, "owner", Membership.Role.OWNER)
        cls.operator = cls.create_member(cls.workspace, "operator", Membership.Role.OPERATOR)
        cls.reviewer = cls.create_member(cls.workspace, "reviewer", Membership.Role.REVIEWER)
        cls.viewer = cls.create_member(cls.workspace, "viewer", Membership.Role.VIEWER)
        cls.other_owner = cls.create_member(cls.other_workspace, "other-owner", Membership.Role.OWNER)
        cls.asset = Asset.objects.create(
            workspace=cls.workspace, name="示例资产甲", system="业务系统甲", owner_team="示例团队",
        )
        cls.other_asset = Asset.objects.create(
            workspace=cls.other_workspace, name="示例资产乙", system="业务系统乙", owner_team="示例团队",
        )
        cls.requirement = Requirement.objects.create(
            code="G04",
            title="网络安全等级保护与日志",
            citation="《中华人民共和国网络安全法》第23条",
            source_url="https://www.cac.gov.cn/2025-12/29/c_1768735112911946.htm",
            summary="按规定留存相关网络日志。",
        )
        cls.check = Check.objects.create(
            asset=cls.asset, requirement=cls.requirement, owner_team="示例团队",
            note="抽查留存配置和日志样本", created_by=cls.operator,
        )
        cls.other_check = Check.objects.create(
            asset=cls.other_asset, requirement=cls.requirement, owner_team="示例团队",
            note="独立租户检查记录", created_by=cls.other_owner, submitted_by=cls.other_owner,
            status=Check.Status.IN_REVIEW,
        )
        cls.other_submission = cls.other_check.events.create(
            action=CheckEvent.Action.SUBMITTED, actor=cls.other_owner,
        )

    @staticmethod
    def create_member(workspace, username, role):
        user = get_user_model().objects.create_user(username=username)
        Membership.objects.create(workspace=workspace, user=user, role=role)
        return user

    def check_url(self, name, check=None):
        return reverse(name, kwargs={"workspace": self.workspace.slug, "pk": (check or self.check).pk})

    def check_data(self, asset):
        return {
            "asset": asset.pk,
            "requirement": self.requirement.pk,
            "owner_team": "示例团队",
            "note": "测试日志留存配置",
        }

    @staticmethod
    def evidence_data(kind=Evidence.Kind.OBSERVATION, record="LOG-001", *, observed_at=None):
        return {
            "kind": kind,
            "source_system": "测试日志平台",
            "source_record": record,
            "summary": "配置及抽样结果已核对",
            "observed_at": (observed_at or timezone.now()).isoformat(),
        }

    def review_data(self, *, decision="verified", note="测试复核", check=None, revision=None):
        if revision is None:
            revision = (check or self.check).events.filter(action=CheckEvent.Action.SUBMITTED).first().pk
        return {"decision": decision, "note": note, "review_revision": revision}

    def test_anonymous_access_redirects_to_login(self):
        url = self.check_url("check_detail")
        response = self.client.get(url)
        self.assertRedirects(response, f"{reverse('login')}?next={url}", fetch_redirect_response=False)

    def test_authenticated_owner_cannot_access_workspace_without_membership(self):
        self.client.force_login(self.owner)
        for name in ("dashboard", "assets", "activities", "checks", "asset_new", "check_new"):
            with self.subTest(name=name):
                url = reverse(name, kwargs={"workspace": self.other_workspace.slug})
                self.assertEqual(self.client.get(url).status_code, 404)

    def test_foreign_check_is_inaccessible_through_own_workspace(self):
        self.client.force_login(self.owner)
        routes = (
            ("get", "check_detail", {}),
            ("get", "check_export", {}),
            ("get", "check_evidence", {}),
            ("post", "check_evidence", self.evidence_data()),
            ("post", "check_submit", {}),
            ("post", "check_review", self.review_data(revision=self.other_submission.pk)),
        )
        for method, name, data in routes:
            with self.subTest(method=method, name=name):
                response = getattr(self.client, method)(self.check_url(name, self.other_check), data)
                self.assertEqual(response.status_code, 404)
        self.other_check.refresh_from_db()
        self.assertEqual(self.other_check.status, Check.Status.IN_REVIEW)
        self.assertFalse(self.other_check.evidence.exists())
        self.assertEqual(list(self.other_check.events.values_list("pk", flat=True)), [self.other_submission.pk])

    def test_creation_rejects_assets_from_other_workspace(self):
        self.client.force_login(self.owner)
        forms = (
            ("check_new", self.check_data(self.other_asset)),
            ("activity_new", {
                "asset": self.other_asset.pk,
                "name": "试用内部模型",
                "kind": Activity.Kind.AI,
                "purpose": "检索测试记录",
            }),
        )
        for name, data in forms:
            with self.subTest(name=name):
                url = reverse(name, kwargs={"workspace": self.workspace.slug})
                response = self.client.post(url, data)
                self.assertEqual(response.status_code, 200)
                self.assertIn("asset", response.context["form"].errors)
        self.assertEqual(Check.objects.count(), 2)
        self.assertFalse(Activity.objects.exists())

    def test_viewer_can_read_and_cannot_mutate(self):
        self.client.force_login(self.viewer)
        self.assertEqual(self.client.get(self.check_url("check_detail")).status_code, 200)
        workspace = {"workspace": self.workspace.slug}
        posts = (
            (reverse("asset_new", kwargs=workspace), {
                "name": "另一个资产", "system": "示例系统", "owner_team": "示例团队",
                "category": Asset.Category.UNKNOWN, "business_level": Asset.BusinessLevel.UNKNOWN,
                "statutory_category": Asset.StatutoryCategory.UNKNOWN,
            }),
            (reverse("activity_new", kwargs=workspace), {
                "asset": self.asset.pk, "name": "试用内部模型",
                "kind": Activity.Kind.AI, "purpose": "检索测试记录",
            }),
            (reverse("check_new", kwargs=workspace), self.check_data(self.asset)),
            (self.check_url("check_evidence"), self.evidence_data()),
            (self.check_url("check_submit"), {}),
            (self.check_url("check_review"), self.review_data(revision=self.other_submission.pk)),
        )
        for url, data in posts:
            with self.subTest(url=url):
                self.assertEqual(self.client.post(url, data).status_code, 403)
        self.check.refresh_from_db()
        self.assertEqual(self.check.status, Check.Status.DRAFT)
        self.assertFalse(self.check.evidence.exists())
        self.assertFalse(self.check.events.exists())
        self.assertEqual(Check.objects.count(), 2)
        self.assertEqual(Asset.objects.count(), 2)
        self.assertFalse(Activity.objects.exists())

    def test_owner_and_operator_can_create_checks_and_submit_with_evidence(self):
        for user in (self.owner, self.operator):
            with self.subTest(user=user.username):
                self.client.force_login(user)
                url = reverse("check_new", kwargs={"workspace": self.workspace.slug})
                data = self.check_data(self.asset)
                data["note"] = f"由 {user.username} 创建的测试检查"
                response = self.client.post(url, data)
                check = Check.objects.get(asset=self.asset, note=data["note"])
                detail = self.check_url("check_detail", check)
                self.assertRedirects(response, detail)
                self.assertEqual(check.created_by, user)
                self.assertEqual(check.status, Check.Status.DRAFT)

                response = self.client.post(self.check_url("check_evidence", check), self.evidence_data())
                self.assertRedirects(response, detail)
                self.assertEqual(check.evidence.get().created_by, user)

                response = self.client.post(self.check_url("check_submit", check))
                self.assertRedirects(response, detail)
                check.refresh_from_db()
                self.assertEqual(check.status, Check.Status.IN_REVIEW)
                self.assertEqual(check.submitted_by, user)
                self.assertEqual(check.events.get(action=CheckEvent.Action.SUBMITTED).actor, user)

    def test_submit_without_evidence_leaves_check_in_draft(self):
        self.client.force_login(self.operator)
        self.client.post(self.check_url("check_submit"))
        self.check.refresh_from_db()
        self.assertEqual(self.check.status, Check.Status.DRAFT)
        self.assertIsNone(self.check.submitted_by)
        self.assertFalse(self.check.events.filter(action=CheckEvent.Action.SUBMITTED).exists())

    def test_owner_cannot_review_a_check_they_created_or_submitted(self):
        self.client.force_login(self.owner)
        for creator, submitter in ((self.owner, self.operator), (self.operator, self.owner)):
            with self.subTest(creator=creator.username, submitter=submitter.username):
                check = Check.objects.create(
                    asset=self.asset, requirement=self.requirement, owner_team="示例团队",
                    note="复核职责分离", status=Check.Status.IN_REVIEW,
                    created_by=creator, submitted_by=submitter,
                )
                submission = check.events.create(action=CheckEvent.Action.SUBMITTED, actor=submitter)
                response = self.client.post(
                    self.check_url("check_review", check), self.review_data(check=check),
                )
                self.assertEqual(response.status_code, 403)
                check.refresh_from_db()
                self.assertEqual(check.status, Check.Status.IN_REVIEW)
                self.assertEqual(list(check.events.values_list("pk", flat=True)), [submission.pk])

    def test_owner_cannot_review_evidence_they_authored(self):
        detail = self.check_url("check_detail")
        review = self.check_url("check_review")
        self.client.force_login(self.owner)
        self.assertRedirects(self.client.post(self.check_url("check_evidence"), self.evidence_data()), detail)
        self.assertEqual(self.check.evidence.get().created_by, self.owner)

        self.client.force_login(self.operator)
        self.assertRedirects(self.client.post(self.check_url("check_submit")), detail)

        self.client.force_login(self.owner)
        self.assertEqual(self.client.get(review).status_code, 403)
        self.assertEqual(self.client.post(review, self.review_data()).status_code, 403)
        self.check.refresh_from_db()
        self.assertEqual(self.check.status, Check.Status.IN_REVIEW)
        self.assertFalse(self.check.events.filter(action=CheckEvent.Action.VERIFIED).exists())

        self.client.force_login(self.reviewer)
        self.assertRedirects(self.client.post(review, self.review_data()), detail)
        self.check.refresh_from_db()
        self.assertEqual(self.check.status, Check.Status.VERIFIED)

    def test_stale_review_form_cannot_decide_a_later_submission(self):
        self.check.status = Check.Status.IN_REVIEW
        self.check.submitted_by = self.operator
        self.check.save(update_fields=["status", "submitted_by"])
        first = self.check.events.create(action=CheckEvent.Action.SUBMITTED, actor=self.operator)
        review = self.check_url("check_review")
        self.client.force_login(self.reviewer)

        response = self.client.get(review)
        self.assertContains(response, 'name="review_revision"')
        field = response.context["form"]["review_revision"]
        self.assertTrue(field.is_hidden)
        self.assertEqual(str(field.value()), str(first.pk))
        stale_review = self.review_data(revision=field.value())

        self.check.events.create(action=CheckEvent.Action.CHANGES, actor=self.reviewer)
        latest = self.check.events.create(action=CheckEvent.Action.SUBMITTED, actor=self.operator)
        response = self.client.post(review, stale_review)
        self.assertEqual(response.status_code, 409)
        self.check.refresh_from_db()
        self.assertEqual(self.check.status, Check.Status.IN_REVIEW)
        self.assertFalse(self.check.events.filter(action=CheckEvent.Action.VERIFIED).exists())

        response = self.client.post(review, self.review_data(revision=latest.pk))
        self.assertRedirects(response, self.check_url("check_detail"))
        self.check.refresh_from_db()
        self.assertEqual(self.check.status, Check.Status.VERIFIED)

    def test_changes_require_retest_evidence_entered_and_observed_after_review(self):
        detail = self.check_url("check_detail")
        evidence = self.check_url("check_evidence")
        submit = self.check_url("check_submit")
        review = self.check_url("check_review")
        self.client.force_login(self.operator)
        self.client.post(evidence, self.evidence_data(Evidence.Kind.RETEST, "RETEST-OLD"))
        self.assertRedirects(self.client.post(submit), detail)

        self.client.force_login(self.reviewer)
        response = self.client.post(review, self.review_data(decision="changes", note="请补充整改后的复测"))
        self.assertRedirects(response, detail)
        self.check.refresh_from_db()
        self.assertEqual(self.check.status, Check.Status.NEEDS_WORK)
        changes = self.check.events.get(action=CheckEvent.Action.CHANGES)
        self.assertEqual(changes.actor, self.reviewer)
        self.assertEqual(changes.note, "请补充整改后的复测")
        # A clock-ahead observation still cannot make previously entered evidence new.
        self.check.evidence.filter(source_record="RETEST-OLD").update(
            observed_at=changes.created_at + timedelta(seconds=1),
        )

        self.client.force_login(self.operator)
        self.client.post(evidence, self.evidence_data(Evidence.Kind.REMEDIATION, "FIX-001"))
        self.client.post(submit)
        self.check.refresh_from_db()
        self.assertEqual(self.check.status, Check.Status.NEEDS_WORK)
        self.assertEqual(self.check.events.filter(action=CheckEvent.Action.SUBMITTED).count(), 1)

        self.assertRedirects(self.client.post(evidence, self.evidence_data(
            Evidence.Kind.RETEST, "RETEST-BACKDATED", observed_at=changes.created_at - timedelta(seconds=1),
        )), detail)
        self.client.post(submit)
        self.check.refresh_from_db()
        self.assertEqual(self.check.status, Check.Status.NEEDS_WORK)
        self.assertEqual(self.check.events.filter(action=CheckEvent.Action.SUBMITTED).count(), 1)

        self.client.post(evidence, self.evidence_data(Evidence.Kind.RETEST, "RETEST-NEW"))
        self.assertRedirects(self.client.post(submit), detail)
        self.check.refresh_from_db()
        self.assertEqual(self.check.status, Check.Status.IN_REVIEW)
        self.assertEqual(self.check.submitted_by, self.operator)
        self.assertEqual(self.check.events.filter(action=CheckEvent.Action.SUBMITTED).count(), 2)

        self.client.force_login(self.reviewer)
        self.client.post(review, self.review_data(note=""))
        self.check.refresh_from_db()
        self.assertEqual(self.check.status, Check.Status.IN_REVIEW)
        self.assertFalse(self.check.events.filter(action=CheckEvent.Action.VERIFIED).exists())

        response = self.client.post(review, self.review_data(note="复测结果符合检查标准"))
        self.assertRedirects(response, detail)
        self.check.refresh_from_db()
        self.assertEqual(self.check.status, Check.Status.VERIFIED)
        verified = self.check.events.get(action=CheckEvent.Action.VERIFIED)
        self.assertEqual(verified.actor, self.reviewer)
        self.assertEqual(verified.note, "复测结果符合检查标准")

    def test_check_export_returns_control_report_json(self):
        self.client.force_login(self.operator)
        self.client.post(self.check_url("check_evidence"), self.evidence_data())
        self.client.force_login(self.viewer)

        response = self.client.get(self.check_url("check_export"))
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.headers["Content-Type"].split(";")[0], "application/json")
        report = response.json()
        self.assertIsInstance(report, dict)
        report_text = str(report)
        self.assertIn(self.requirement.code, report_text)
        self.assertIn(self.asset.name, report_text)
        self.assertIn("LOG-001", report_text)
        self.assertNotIn(self.other_asset.name, report_text)
