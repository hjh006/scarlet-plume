from django.urls import path

from . import views

urlpatterns = [
    path("", views.home, name="home"),
    path("workspaces/<slug:workspace>/", views.dashboard, name="dashboard"),
    path("workspaces/<slug:workspace>/assets/", views.assets, name="assets"),
    path("workspaces/<slug:workspace>/assets/new/", views.asset_new, name="asset_new"),
    path("workspaces/<slug:workspace>/activities/", views.activities, name="activities"),
    path("workspaces/<slug:workspace>/activities/new/", views.activity_new, name="activity_new"),
    path("workspaces/<slug:workspace>/checks/", views.checks, name="checks"),
    path("workspaces/<slug:workspace>/checks/new/", views.check_new, name="check_new"),
    path("workspaces/<slug:workspace>/checks/<int:pk>/", views.check_detail, name="check_detail"),
    path("workspaces/<slug:workspace>/checks/<int:pk>/evidence/", views.check_evidence, name="check_evidence"),
    path("workspaces/<slug:workspace>/checks/<int:pk>/submit/", views.check_submit, name="check_submit"),
    path("workspaces/<slug:workspace>/checks/<int:pk>/review/", views.check_review, name="check_review"),
    path("workspaces/<slug:workspace>/checks/<int:pk>/export/", views.check_export, name="check_export"),
]
