from functools import wraps

from django.contrib import messages
from django.contrib.auth.decorators import login_required
from django.core.exceptions import PermissionDenied
from django.db import IntegrityError, transaction
from django.db.models import Count
from django.http import HttpResponse, JsonResponse
from django.shortcuts import get_object_or_404, redirect, render
from django.urls import reverse
from django.utils import timezone
from django.views.decorators.cache import never_cache
from django.views.decorators.http import require_GET, require_http_methods, require_POST

from .forms import ActivityForm, AssetForm, CheckForm, EvidenceForm, ReviewForm
from .models import Activity, Asset, Check, CheckEvent, Evidence, Membership, Requirement


def workspace_required(view):
    @wraps(view)
    @never_cache
    @login_required
    def scoped_view(request, workspace, *args, **kwargs):
        membership = get_object_or_404(
            Membership.objects.select_related("workspace"), workspace__slug=workspace, user=request.user,
        )
        return view(request, membership, *args, **kwargs)
    return scoped_view


def context(membership, **values):
    return {
        "workspace": membership.workspace, "membership": membership,
        "can_edit": membership.can_edit, **values,
    }


def workspace_checks(membership):
    return Check.objects.filter(asset__workspace_id=membership.workspace_id).select_related(
        "asset", "requirement",
    )


def current_check(membership, pk, *, lock=False):
    checks = workspace_checks(membership)
    if lock:
        checks = checks.select_for_update(of=("self",))
    return get_object_or_404(checks, pk=pk)


def editor_only(membership):
    if not membership.can_edit:
        raise PermissionDenied


def form_page(request, membership, form, title, description, submit_label, cancel_url):
    return render(request, "governance/form.html", context(
        membership, form=form, title=title, description=description,
        submit_label=submit_label, cancel_url=cancel_url,
    ))


@require_GET
@never_cache
@login_required
def home(request):
    memberships = list(Membership.objects.filter(user=request.user).select_related("workspace").order_by(
        "workspace__name", "workspace__slug",
    ))
    if len(memberships) == 1:
        return redirect("dashboard", workspace=memberships[0].workspace.slug)
    return render(request, "governance/home.html", {"memberships": memberships})


@require_GET
@workspace_required
def dashboard(request, membership):
    checks = workspace_checks(membership)
    counts = dict(checks.order_by().values_list("status").annotate(total=Count("pk")))
    metrics = [
        {"kind": "assets", "label": "数据资产", "value": Asset.objects.filter(workspace_id=membership.workspace_id).count(),
         "detail": "已登记资产"},
        {"kind": "in_review", "label": "待复核", "value": counts.get(Check.Status.IN_REVIEW, 0), "detail": "等待独立复核"},
        {"kind": "needs_work", "label": "待整改", "value": counts.get(Check.Status.NEEDS_WORK, 0), "detail": "需要提交复测证据"},
        {"kind": "verified", "label": "已验证", "value": counts.get(Check.Status.VERIFIED, 0), "detail": "通过本次检查"},
    ]
    return render(request, "governance/dashboard.html", context(
        membership, metrics=metrics, recent_checks=checks[:8], requirements=Requirement.objects.all(),
    ))


@require_GET
@workspace_required
def assets(request, membership):
    return render(request, "governance/assets.html", context(
        membership, assets=Asset.objects.filter(workspace_id=membership.workspace_id),
    ))


@require_http_methods(["GET", "POST"])
@workspace_required
def asset_new(request, membership):
    editor_only(membership)
    form = AssetForm(request.POST if request.method == "POST" else None)
    if form.is_valid():
        asset = form.save(commit=False)
        asset.workspace = membership.workspace
        try:
            with transaction.atomic():
                asset.save()
        except IntegrityError:
            if not Asset.objects.filter(
                workspace=asset.workspace, system=asset.system, name=asset.name,
            ).exists():
                raise
            form.add_error("name", "该来源系统内已登记同名资产。")
        else:
            messages.success(request, "已登记数据资产。")
            return redirect("assets", workspace=membership.workspace.slug)
    return form_page(
        request, membership, form, "登记数据资产", "登记来源、数据类别和责任团队。",
        "保存资产", reverse("assets", kwargs={"workspace": membership.workspace.slug}),
    )


@require_GET
@workspace_required
def activities(request, membership):
    return render(request, "governance/activities.html", context(
        membership, activities=Activity.objects.filter(asset__workspace_id=membership.workspace_id)
        .select_related("asset"),
    ))


@require_http_methods(["GET", "POST"])
@workspace_required
def activity_new(request, membership):
    editor_only(membership)
    form = ActivityForm(request.POST if request.method == "POST" else None, workspace=membership.workspace)
    if form.is_valid():
        form.save()
        messages.success(request, "已登记处理活动。")
        return redirect("activities", workspace=membership.workspace.slug)
    return form_page(
        request, membership, form, "登记处理活动", "登记内部处理、对外共享或 AI 使用。",
        "保存活动", reverse("activities", kwargs={"workspace": membership.workspace.slug}),
    )


@require_GET
@workspace_required
def checks(request, membership):
    selected = request.GET.get("status", "")
    rows = workspace_checks(membership)
    if selected in Check.Status.values:
        rows = rows.filter(status=selected)
    else:
        selected = ""
    return render(request, "governance/checks.html", context(
        membership, checks=rows, status_options=Check.Status.choices, selected_status=selected,
    ))


@require_http_methods(["GET", "POST"])
@workspace_required
def check_new(request, membership):
    editor_only(membership)
    form = CheckForm(request.POST if request.method == "POST" else None, workspace=membership.workspace)
    if form.is_valid():
        with transaction.atomic():
            check = form.save(commit=False)
            check.created_by = request.user
            check.save()
            check.events.create(action=CheckEvent.Action.CREATED, actor=request.user)
        messages.success(request, "已创建检查，可以开始登记证据。")
        return redirect("check_detail", workspace=membership.workspace.slug, pk=check.pk)
    return form_page(
        request, membership, form, "新建控制检查", "选定监管要求和资产，再写明本次检查的范围与通过标准。",
        "创建检查", reverse("checks", kwargs={"workspace": membership.workspace.slug}),
    )


def has_submission_evidence(check):
    if check.status == Check.Status.DRAFT:
        return check.evidence.exists()
    changes = check.events.filter(action=CheckEvent.Action.CHANGES).first()
    return bool(changes and check.evidence.filter(
        kind=Evidence.Kind.RETEST, created_at__gt=changes.created_at, observed_at__gt=changes.created_at,
    ).exists())


@require_GET
@workspace_required
def check_detail(request, membership, pk):
    check = current_check(membership, pk)
    can_add_evidence = membership.can_edit and check.status in (Check.Status.DRAFT, Check.Status.NEEDS_WORK)
    return render(request, "governance/check_detail.html", context(
        membership, check=check, evidence_list=check.evidence.select_related("created_by"),
        events=check.events.select_related("actor"),
        can_add_evidence=can_add_evidence, can_submit=can_add_evidence and has_submission_evidence(check),
        can_review=check.can_review_with(membership),
    ))


@require_http_methods(["GET", "POST"])
@workspace_required
def check_evidence(request, membership, pk):
    check = current_check(membership, pk)
    editor_only(membership)
    if check.status not in (Check.Status.DRAFT, Check.Status.NEEDS_WORK):
        return HttpResponse("当前状态不能登记证据。", status=409)

    form = EvidenceForm(request.POST if request.method == "POST" else None)
    if form.is_valid():
        with transaction.atomic():
            check = current_check(membership, pk, lock=True)
            if check.status not in (Check.Status.DRAFT, Check.Status.NEEDS_WORK):
                return HttpResponse("检查状态已变更，请刷新。", status=409)
            evidence = form.save(commit=False)
            evidence.control_check = check
            evidence.created_by = request.user
            evidence.save()
            check.events.create(
                action=CheckEvent.Action.EVIDENCE, actor=request.user, note=evidence.get_kind_display(),
            )
            check.save(update_fields=["updated_at"])
        messages.success(request, "已登记脱敏证据。")
        return redirect("check_detail", workspace=membership.workspace.slug, pk=pk)
    return form_page(
        request, membership, form, "登记证据", "记录客户源系统内的引用、时间和脱敏结果。",
        "保存证据", reverse("check_detail", kwargs={"workspace": membership.workspace.slug, "pk": pk}),
    )


@require_POST
@workspace_required
def check_submit(request, membership, pk):
    with transaction.atomic():
        check = current_check(membership, pk, lock=True)
        editor_only(membership)
        if check.status not in (Check.Status.DRAFT, Check.Status.NEEDS_WORK):
            return HttpResponse("当前状态不能提交复核。", status=409)
        if not has_submission_evidence(check):
            messages.error(request, "请先登记证据。已要求整改的检查需要新增复测记录。")
            return redirect("check_detail", workspace=membership.workspace.slug, pk=pk)
        check.status = Check.Status.IN_REVIEW
        check.submitted_by = request.user
        check.save(update_fields=["status", "submitted_by", "updated_at"])
        check.events.create(action=CheckEvent.Action.SUBMITTED, actor=request.user)
    messages.success(request, "已提交，等待独立复核。")
    return redirect("check_detail", workspace=membership.workspace.slug, pk=pk)


@require_http_methods(["GET", "POST"])
@workspace_required
def check_review(request, membership, pk):
    check = current_check(membership, pk)
    if not check.can_review_with(membership):
        raise PermissionDenied
    submission = check.events.filter(action=CheckEvent.Action.SUBMITTED).first()
    if submission is None:
        return HttpResponse("缺少提交记录。", status=409)
    form = ReviewForm(
        request.POST if request.method == "POST" else None, initial={"review_revision": submission.pk},
    )
    if form.is_valid():
        with transaction.atomic():
            check = current_check(membership, pk, lock=True)
            if not check.can_review_with(membership):
                raise PermissionDenied
            latest_submission = check.events.filter(action=CheckEvent.Action.SUBMITTED).first()
            if latest_submission is None or form.cleaned_data["review_revision"] != latest_submission.pk:
                return HttpResponse("提交内容已更新，请打开本次复核。", status=409)
            verified = form.cleaned_data["decision"] == "verified"
            check.status = Check.Status.VERIFIED if verified else Check.Status.NEEDS_WORK
            check.save(update_fields=["status", "updated_at"])
            check.events.create(
                action=CheckEvent.Action.VERIFIED if verified else CheckEvent.Action.CHANGES,
                actor=request.user, note=form.cleaned_data["note"],
            )
        messages.success(request, "已记录复核结果。")
        return redirect("check_detail", workspace=membership.workspace.slug, pk=pk)
    return form_page(
        request, membership, form, "独立复核", "核对源系统证据，记录通过理由或整改要求。",
        "保存复核结果", reverse("check_detail", kwargs={"workspace": membership.workspace.slug, "pk": pk}),
    )


@require_GET
@workspace_required
@transaction.atomic
def check_export(request, membership, pk):
    check = current_check(membership, pk, lock=True)
    response = JsonResponse({
        "workspace": membership.workspace.slug,
        "exported_at": timezone.now(),
        "check": {
            "id": check.pk, "asset": check.asset.name, "system": check.asset.system,
            "status": check.status, "owner_team": check.owner_team, "note": check.note,
            "created_at": check.created_at, "updated_at": check.updated_at,
        },
        "requirement": {
            "code": check.requirement.code, "title": check.requirement.title,
            "citation": check.requirement.citation, "source_url": check.requirement.source_url,
        },
        "evidence": [
            {
                "kind": entry.kind, "source_system": entry.source_system, "source_record": entry.source_record,
                "summary": entry.summary, "observed_at": entry.observed_at, "created_at": entry.created_at,
                "recorded_by": entry.created_by.get_username(),
            }
            for entry in check.evidence.select_related("created_by")
        ],
        "events": [
            {"action": entry.action, "note": entry.note, "created_at": entry.created_at,
             "actor": entry.actor.get_username()}
            for entry in check.events.select_related("actor")
        ],
    }, json_dumps_params={"ensure_ascii": False, "indent": 2})
    response["Content-Disposition"] = f'attachment; filename="control-{check.pk}.json"'
    return response
