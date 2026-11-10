import os
from getpass import getpass

from django.contrib.auth import get_user_model, password_validation
from django.core.exceptions import ValidationError
from django.core.management.base import BaseCommand, CommandError
from django.core.validators import validate_slug
from django.db import transaction

from governance.catalog import REQUIREMENTS
from governance.models import Membership, Requirement, Workspace


class Command(BaseCommand):
    help = "创建工作区或为现有工作区添加成员。"

    def add_arguments(self, parser):
        parser.add_argument("slug")
        parser.add_argument("name")
        parser.add_argument("username")
        parser.add_argument("--role", choices=Membership.Role.values, default=Membership.Role.OWNER)

    def handle(self, *args, **options):
        slug, name, username = (options[key].strip() for key in ("slug", "name", "username"))
        try:
            validate_slug(slug)
        except ValidationError as error:
            raise CommandError("工作区标识只允许字母、数字、连字符和下划线。") from error
        if len(slug) > 60 or not 1 <= len(name) <= 100:
            raise CommandError("请检查工作区标识和名称的长度。")

        user_model = get_user_model()
        user = user_model.objects.filter(username=username).first()
        if user is None:
            user = user_model(username=username)
            password = os.environ.get("APP_PROVISION_PASSWORD")
            if password is None:
                password = getpass("新账号密码：")
                if password != getpass("确认密码："):
                    raise CommandError("两次密码不同。")
            try:
                user.full_clean(exclude=["password"])
                password_validation.validate_password(password, user)
            except ValidationError as error:
                raise CommandError(" ".join(error.messages)) from error
            user.set_password(password)

        with transaction.atomic():
            workspace, _ = Workspace.objects.get_or_create(slug=slug, defaults={"name": name})
            if workspace.name != name:
                raise CommandError("工作区标识已存在，名称不匹配。")
            if user.pk is None:
                user.save()
            membership, created = Membership.objects.get_or_create(
                workspace=workspace, user=user, defaults={"role": options["role"]},
            )
            if not created and membership.role != options["role"]:
                raise CommandError("成员已有角色，请先确认工作区成员的权限安排。")
            for entry in REQUIREMENTS:
                Requirement.objects.get_or_create(
                    code=entry["code"], defaults={key: value for key, value in entry.items() if key != "code"},
                )
        self.stdout.write(self.style.SUCCESS(f"{workspace.slug} · {user.get_username()} · {membership.role}"))
