"""Run Django's HTTP test client against the ephemeral PostgreSQL service."""

from .settings import *


if (
    APP_ENV != "production"
    or DATABASES["default"]["HOST"] != "127.0.0.1"
    or DATABASES["default"]["NAME"] != "governance_ci"
):
    raise ImproperlyConfigured("The PostgreSQL test settings require the local CI database.")

DATABASES = {"default": {**DATABASES["default"], "OPTIONS": {"sslmode": "disable"}}}
SECURE_SSL_REDIRECT = False
