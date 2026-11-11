FROM wazuh/wazuh-manager:4.14.10@sha256:dcda4869e2f792e92f03bce93f85b4bdf103756ea61cfca8ba9f3eabeb05365b
COPY manager.conf /wazuh-config-mount/etc/ossec.conf
COPY scarlet_rules.xml /wazuh-config-mount/etc/rules/scarlet_rules.xml
COPY custom-scarlet-export /wazuh-config-mount/integrations/custom-scarlet-export
COPY project.py /opt/scarlet/project.py
COPY init-permissions /etc/cont-init.d/2-scarlet-permissions
RUN rm -rf /etc/services.d/filebeat && mkdir -p /scarlet-spool/pending && chown -R root:wazuh /scarlet-spool && chmod 770 /scarlet-spool /scarlet-spool/pending && chmod 755 /etc/cont-init.d/2-scarlet-permissions && chmod 750 /wazuh-config-mount/integrations/custom-scarlet-export
